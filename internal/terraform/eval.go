package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EvalJSON evaluates the given HCL expression in the context of the project's
// Terraform console and attempts to parse the result as JSON by wrapping it in
// jsonencode(). Returns (value, true) on success; otherwise (nil, false).
// workDir should be the scratch dir used by the console so files and modules match.
func EvalJSON(workDir, statePath string, varFiles []string, expr string, timeout time.Duration) (any, bool) {
	// Protect against empty expressions
	e := strings.TrimSpace(expr)
	if e == "" {
		return nil, false
	}
	// Zero-cost fast path: in-process HCL evaluation for simple var/local expressions
	if v, ok := TryEvalInProcess(workDir, varFiles, e, timeout); ok {
		return v, true
	}
	// Try persistent evaluator first for speed
	if pe := getOrStartPersistentEvaluator(workDir, statePath, varFiles); pe != nil {
		if v, ok := pe.EvaluateJSON(e, timeout); ok {
			return v, true
		}
	}
	// Wrap in jsonencode to force machine-readable output
	line := "jsonencode(" + e + ")"
	// Use a read-only snapshot of the state to avoid lock contention with our writer
	snap := statePath
	if fi, err := os.Stat(statePath); err == nil && !fi.IsDir() {
		tmp := filepath.Join(filepath.Dir(statePath), ".tfstate-eval-"+time.Now().Format("20060102T150405.000000000"))
		if err := copyFile(statePath, tmp, 0o600); err == nil {
			snap = tmp
			defer func() { _ = os.Remove(tmp) }()
		}
	}
	s := StartConsoleSession(workDir, snap, varFiles)
	stdout, _, err := s.Evaluate(line, timeout)
	if err != nil {
		return nil, false
	}
	out := strings.TrimSpace(stdout)
	if out == "" {
		return nil, false
	}
	var v any
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		return nil, false
	}
	return v, true
}
