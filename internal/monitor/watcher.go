package monitor

import (
	"os"
	"path/filepath"
	"time"
)

var watchExtensions = []string{".tf", ".tfvars"}

// WatchTerraformFilesNotifying receives a channel and sends a notification on changes
func WatchTerraformFilesNotifying(dir string, refreshCh chan<- struct{}) {
	last := map[string]time.Time{}
	// Debounce bursts of edits within this interval
	const debounce = 500 * time.Millisecond
	var pending bool
	var lastFire time.Time
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if pollTerraformFiles(dir, last) {
				pending = true
			}
			if pending && time.Since(lastFire) >= debounce {
				select {
				case refreshCh <- struct{}{}:
					lastFire = time.Now()
					pending = false
				default:
					// channel full, skip
				}
			}
		}
	}()
}

func pollTerraformFiles(dir string, last map[string]time.Time) bool {
	changed := false
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		for _, ext := range watchExtensions {
			if filepath.Ext(path) == ext {
				mod := info.ModTime()
				if last[path].IsZero() {
					last[path] = mod
				} else if mod.After(last[path]) {
					last[path] = mod
					changed = true
				}
				break
			}
		}
		return nil
	})
	return changed
}
