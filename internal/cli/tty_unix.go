//go:build darwin || linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// acquireTTY opens /dev/tty and sets raw -echo mode, returning the tty file and a restore func.
func acquireTTY() (*os.File, func(), error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/tty: %w", err)
	}
	// Save current settings
	var save *exec.Cmd
	if runtime.GOOS == "darwin" {
		save = exec.Command("stty", "-f", "/dev/tty", "-g")
	} else {
		save = exec.Command("stty", "-g")
	}
	save.Stdin = tty
	out, err := save.Output()
	if err != nil {
		if cerr := tty.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "close tty after stty -g error:", cerr)
		}
		return nil, nil, fmt.Errorf("stty -g failed: %w", err)
	}
	state := strings.TrimSpace(string(out))
	// Enable raw, -echo
	var raw *exec.Cmd
	if runtime.GOOS == "darwin" {
		raw = exec.Command("stty", "-f", "/dev/tty", "raw", "-echo")
	} else {
		raw = exec.Command("stty", "raw", "-echo")
	}
	raw.Stdin = tty
	if err := raw.Run(); err != nil {
		if cerr := tty.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "close tty after stty raw error:", cerr)
		}
		return nil, nil, fmt.Errorf("stty raw -echo failed: %w", err)
	}
	restore := func() {
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			cmd = exec.Command("stty", "-f", "/dev/tty", state)
		} else {
			cmd = exec.Command("stty", state)
		}
		cmd.Stdin = tty
		_ = cmd.Run()
	}
	return tty, restore, nil
}

// detectTermWidth attempts to determine terminal column width on Unix systems.
// Falls back to COLUMNS env or 80 when detection fails.
func detectTermWidth(tty *os.File) int {
	// Prefer stty size; returns "rows cols".
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("stty", "-f", "/dev/tty", "size")
	} else {
		cmd = exec.Command("stty", "size")
	}
	if tty != nil {
		cmd.Stdin = tty
	}
	out, err := cmd.Output()
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(string(out)))
		if len(parts) == 2 {
			// parts[1] is columns
			if n, convErr := atoiSafe(parts[1]); convErr == nil && n > 0 {
				return n
			}
		}
	}
	if c := os.Getenv("COLUMNS"); c != "" {
		if n, err := atoiSafe(c); err == nil && n > 0 {
			return n
		}
	}
	return 80
}

func atoiSafe(s string) (int, error) {
	// Trim and parse without importing strconv globally in multiple files.
	s = strings.TrimSpace(s)
	var n int
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}
