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
		tty.Close()
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
		tty.Close()
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
