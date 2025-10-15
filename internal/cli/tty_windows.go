//go:build windows

package cli

import "os"

// acquireTTY on Windows returns standard input with a no-op restore.
func acquireTTY() (*os.File, func(), error) {
	return os.Stdin, func() {}, nil
}
