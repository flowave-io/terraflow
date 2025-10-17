package terraform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type ConsoleSession struct {
	cmd       *exec.Cmd
	mu        sync.Mutex
	running   bool
	statePath string
	workDir   string
}

// StartConsoleSession creates a new session and records an optional working directory and state path to use with terraform console.
func StartConsoleSession(workDir, statePath string) *ConsoleSession {
	return &ConsoleSession{statePath: statePath, workDir: workDir}
}

// Restart stops any running console and starts a new one, connecting stdio
func (s *ConsoleSession) Restart() { /* no-op with ephemeral evaluation */ }

func (s *ConsoleSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.running {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.running = false
	}
}

// Evaluate runs a short-lived `terraform console`, writes the provided line to stdin,
// and returns the raw stdout and stderr from Terraform. No trimming is applied.
// On timeout, an error is returned; on other non-zero exits, stdout/stderr are
// returned and error is nil so the caller can mirror Terraform output faithfully.
func (s *ConsoleSession) Evaluate(line string, timeout time.Duration) (string, string, error) {
	// Run a short-lived terraform console and pass the expression via stdin
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"console"}
	if sp := s.statePath; sp != "" {
		if fi, err := os.Stat(sp); err == nil && !fi.IsDir() {
			args = append(args, "-state", sp)
		}
	}
	cmd := exec.CommandContext(ctx, "terraform", args...)
	if s.workDir != "" {
		cmd.Dir = s.workDir
	}
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdin = strings.NewReader(line + "\n")
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", errors.New("terraform console evaluation timed out")
	}
	if err != nil {
		// If Terraform produced output on either stream, return it and suppress the error
		if out.Len() > 0 || errBuf.Len() > 0 {
			return out.String(), errBuf.String(), nil
		}
		return "", "", err
	}
	return out.String(), errBuf.String(), nil
}

// Interrupt sends an interrupt signal to the terraform console process (best-effort).
func (s *ConsoleSession) Interrupt() { /* not applicable with ephemeral evaluation */ }
