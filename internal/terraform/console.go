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
	statePath string
	workDir   string

	// precomputed execution details to avoid per-eval overhead
	binPath string
	args    []string
	env     []string
}

var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// StartConsoleSession creates a new ephemeral-eval session that records working directory and state path.
// varFiles are forwarded to `terraform console` as repeated -var-file flags.
func StartConsoleSession(workDir, statePath string, varFiles []string) *ConsoleSession {
	s := &ConsoleSession{statePath: statePath, workDir: workDir}
	// Compute binary path once
	if p, err := exec.LookPath("terraform"); err == nil {
		s.binPath = p
	} else {
		s.binPath = "terraform"
	}
	// Precompute args
	s.args = []string{"console", "-no-color"}
	if sp := s.statePath; sp != "" {
		if fi, err := os.Stat(sp); err == nil && !fi.IsDir() {
			s.args = append(s.args, "-state", sp)
		}
	}
	// Append any provided -var-file flags in the given order
	for _, vf := range varFiles {
		if strings.TrimSpace(vf) == "" {
			continue
		}
		s.args = append(s.args, "-var-file", vf)
	}
	// Precompute env
	env := append([]string{}, os.Environ()...)
	env = append(env, "TF_IN_AUTOMATION=1")
	// Avoid accidental pagers or prompts
	env = append(env, "PAGER=")
	s.env = env
	return s
}

// Restart is a no-op for ephemeral evaluations.
func (s *ConsoleSession) Restart() {}

// Stop is a no-op for ephemeral evaluations.
func (s *ConsoleSession) Stop() {}

// Interrupt is a no-op for ephemeral evaluations.
func (s *ConsoleSession) Interrupt() {}

// Evaluate runs a short-lived `terraform console`, writes the provided line to stdin,
// and returns the raw stdout and stderr from Terraform. No trimming is applied.
// On timeout, an error is returned; on other non-zero exits, stdout/stderr are
// returned and error is nil so the caller can mirror Terraform output faithfully.
func (s *ConsoleSession) Evaluate(line string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	bin := s.binPath
	if bin == "" {
		bin = "terraform"
	}
	cmd := exec.CommandContext(ctx, bin, s.args...)
	if s.workDir != "" {
		cmd.Dir = s.workDir
	}
	// Deterministic, non-interactive environment
	if len(s.env) > 0 {
		cmd.Env = s.env
	} else {
		cmd.Env = os.Environ()
	}

	out := bufferPool.Get().(*bytes.Buffer)
	errBuf := bufferPool.Get().(*bytes.Buffer)
	out.Reset()
	errBuf.Reset()
	defer bufferPool.Put(out)
	defer bufferPool.Put(errBuf)

	cmd.Stdin = strings.NewReader(line + "\n")
	cmd.Stdout = out
	cmd.Stderr = errBuf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", errors.New("terraform console evaluation timed out")
	}
	if err != nil {
		// If Terraform produced output on either stream, return it and suppress the error
		if out.Len() > 0 || errBuf.Len() > 0 {
			sOut := out.String()
			sErr := errBuf.String()
			return sOut, sErr, nil
		}
		return "", "", err
	}
	sOut := out.String()
	sErr := errBuf.String()
	return sOut, sErr, nil
}
