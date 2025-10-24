//go:build windows

package cli

import "os"

// acquireTTY on Windows returns standard input with a no-op restore.
func acquireTTY() (*os.File, func(), error) {
	return os.Stdin, func() {}, nil
}

// detectTermWidth on Windows falls back to COLUMNS env or 80.
func detectTermWidth(_ *os.File) int {
	if c := os.Getenv("COLUMNS"); c != "" {
		// Simple, safe parse
		n := 0
		for i := 0; i < len(c); i++ {
			ch := c[i]
			if ch < '0' || ch > '9' {
				n = 0
				break
			}
			n = n*10 + int(ch-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 80
}
