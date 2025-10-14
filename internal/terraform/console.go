package terraform

import (
	"os"
	"os/exec"
	"sync"
)

type ConsoleSession struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
}

// StartConsoleSession creates a new session and starts the terraform console process
func StartConsoleSession() *ConsoleSession {
	s := &ConsoleSession{}
	s.Restart()
	return s
}

// Restart stops any running console and starts a new one, connecting stdio
func (s *ConsoleSession) Restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.running {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	cmd := exec.Command("terraform", "console")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Start()
	s.cmd = cmd
	s.running = true
}

func (s *ConsoleSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.running {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.running = false
	}
}
