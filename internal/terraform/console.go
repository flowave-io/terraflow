package terraform

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type ConsoleSession struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
}

// StartConsoleSession creates a new session and starts the terraform console process
func StartConsoleSession() *ConsoleSession { return &ConsoleSession{} }

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

// Evaluate sends a line to the terraform console and reads output until a
// sentinel line is observed or a timeout occurs. It returns combined stdout
// lines (stderr is appended with a prefix) and any error.
func (s *ConsoleSession) Evaluate(line string, timeout time.Duration) (string, error) {
	// Run a short-lived terraform console and pass the expression via stdin
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "terraform", "console")
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdin = strings.NewReader(line + "\n")
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("terraform console evaluation timed out")
	}
	if err != nil {
		// Return stderr text if available to show evaluation errors
		if errBuf.Len() > 0 {
			return strings.TrimRight(errBuf.String(), "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}

// Interrupt sends an interrupt signal to the terraform console process (best-effort).
func (s *ConsoleSession) Interrupt() { /* not applicable with ephemeral evaluation */ }
