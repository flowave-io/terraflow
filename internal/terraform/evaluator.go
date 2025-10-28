package terraform

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Persistent evaluator of terraform console for fast non-literal expression resolution.

type persistentEvaluator struct {
	workDir   string
	statePath string // snapshot path used by the evaluator process
	realState string // real state path to snapshot from
	varFiles  []string

	binPath string
	args    []string
	env     []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu      sync.Mutex
	started bool
	closed  bool
	respMu  sync.Mutex
	waiters map[string]chan string
}

var (
	peMu        sync.Mutex
	peInstances = map[string]*persistentEvaluator{}
)

func peKey(workDir, statePath string, varFiles []string) string {
	vv := append([]string{}, varFiles...)
	sort.Strings(vv)
	return workDir + "|" + statePath + "|" + strings.Join(vv, ",")
}

func getOrStartPersistentEvaluator(workDir, statePath string, varFiles []string) *persistentEvaluator {
	key := peKey(workDir, statePath, varFiles)
	peMu.Lock()
	defer peMu.Unlock()
	if pe, ok := peInstances[key]; ok && pe != nil && !pe.closed {
		return pe
	}
	pe := &persistentEvaluator{workDir: workDir, realState: statePath, varFiles: append([]string{}, varFiles...), waiters: map[string]chan string{}}
	peInstances[key] = pe
	return pe
}

func (p *persistentEvaluator) ensureStarted() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started && p.cmd != nil && !p.closed {
		return nil
	}
	// Resolve terraform path
	if p.binPath == "" {
		if bp, err := exec.LookPath("terraform"); err == nil {
			p.binPath = bp
		} else {
			p.binPath = "terraform"
		}
	}
	// Build args
	args := []string{"console", "-no-color"}
	// Prepare a fresh snapshot of the real state to avoid locking the live file
	if rs := strings.TrimSpace(p.realState); rs != "" {
		if fi, err := os.Stat(rs); err == nil && !fi.IsDir() {
			snap := filepath.Join(filepath.Dir(rs), ".tfstate-eval-snapshot.json")
			if copyFile(rs, snap, 0o600) == nil {
				p.statePath = snap
				args = append(args, "-state", snap)
			}
		}
	}
	for _, vf := range p.varFiles {
		if strings.TrimSpace(vf) == "" {
			continue
		}
		args = append(args, "-var-file", vf)
	}
	p.args = args
	env := append([]string{}, os.Environ()...)
	env = append(env, "TF_IN_AUTOMATION=1")
	env = append(env, "PAGER=")
	p.env = env

	cmd := exec.Command(p.binPath, p.args...)
	if p.workDir != "" {
		cmd.Dir = p.workDir
	}
	cmd.Env = p.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Discard stderr; evaluator focuses on JSON returns
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.started = true
	p.closed = false

	go p.readLoop()
	return nil
}

func (p *persistentEvaluator) readLoop() {
	scanner := bufio.NewScanner(p.stdout)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == ">" { // skip empty/prompt
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			if id, _ := m["__id"].(string); id != "" {
				p.respMu.Lock()
				ch := p.waiters[id]
				delete(p.waiters, id)
				p.respMu.Unlock()
				if ch != nil {
					ch <- line
				}
				continue
			}
		}
		// Ignore any other non-JSON lines (banners/prompts/warnings)
	}
	// On exit, close and notify waiters with empty string
	p.respMu.Lock()
	for id, ch := range p.waiters {
		_ = id
		select {
		case ch <- "":
		default:
		}
		close(ch)
	}
	p.waiters = map[string]chan string{}
	p.respMu.Unlock()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

func (p *persistentEvaluator) EvaluateJSON(expr string, timeout time.Duration) (any, bool) {
	if strings.TrimSpace(expr) == "" {
		return nil, false
	}
	if err := p.ensureStarted(); err != nil {
		return nil, false
	}
	id := uuid.NewString()
	// Wrap expression to include unique id and force JSON output
	line := "jsonencode({__id=\"" + id + "\", __val=(" + expr + ")})"

	// Register waiter
	ch := make(chan string, 1)
	p.respMu.Lock()
	p.waiters[id] = ch
	p.respMu.Unlock()

	// Write line
	p.mu.Lock()
	_, werr := io.WriteString(p.stdin, line+"\n")
	p.mu.Unlock()
	if werr != nil {
		return nil, false
	}

	// Await response
	var resp string
	select {
	case resp = <-ch:
	case <-time.After(timeout):
		// Timeout: clean up waiter
		p.respMu.Lock()
		delete(p.waiters, id)
		p.respMu.Unlock()
		return nil, false
	}
	if strings.TrimSpace(resp) == "" {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(resp), &m) != nil {
		return nil, false
	}
	if v, ok := m["__val"]; ok {
		return v, true
	}
	return nil, false
}

func (p *persistentEvaluator) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
	return nil
}

// ResetPersistentEvaluator tears down an evaluator so it restarts on next use.
func ResetPersistentEvaluator(workDir, statePath string, varFiles []string) {
	key := peKey(workDir, statePath, varFiles)
	peMu.Lock()
	pe := peInstances[key]
	delete(peInstances, key)
	peMu.Unlock()
	if pe != nil {
		_ = pe.Close()
	}
}

// ResetAllPersistentEvaluators closes all running evaluators; next eval will restart with fresh snapshot.
func ResetAllPersistentEvaluators() {
	peMu.Lock()
	instances := peInstances
	peInstances = map[string]*persistentEvaluator{}
	peMu.Unlock()
	for _, pe := range instances {
		if pe != nil {
			_ = pe.Close()
		}
	}
}

// UpdatePersistentEvaluatorSnapshots updates the evaluator snapshot files for any
// evaluators bound to the given real state path, so they immediately see latest state
// without restarting. The write is atomic (tmp + rename) with 0600 permissions.
func UpdatePersistentEvaluatorSnapshots(realStatePath string, stateBytes []byte) {
	peMu.Lock()
	instances := make([]*persistentEvaluator, 0, len(peInstances))
	for _, pe := range peInstances {
		if pe != nil && !pe.closed && pe.realState == realStatePath && strings.TrimSpace(pe.statePath) != "" {
			instances = append(instances, pe)
		}
	}
	peMu.Unlock()
	for _, pe := range instances {
		tmp := pe.statePath + ".tmp-" + time.Now().Format("20060102T150405.000000000")
		_ = os.WriteFile(tmp, stateBytes, 0o600)
		_ = os.Rename(tmp, pe.statePath)
	}
}
