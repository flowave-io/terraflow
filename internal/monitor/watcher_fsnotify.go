//go:build fsnotify

package monitor

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchTerraformFilesNotifying (fsnotify build) uses OS events for instant refreshes.
func WatchTerraformFilesNotifying(dir string, refreshCh chan<- struct{}) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		// Should not happen under fsnotify build, but guard anyway
		go func() { refreshCh <- struct{}{} }()
		return
	}
	go func() {
		defer w.Close()
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				_ = w.Add(path)
			}
			return nil
		})
		const debounce = 75 * time.Millisecond
		var pending bool
		var lastFire time.Time
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if matchesExt(ev.Name) {
					pending = true
				}
				if pending && time.Since(lastFire) >= debounce {
					select {
					case refreshCh <- struct{}{}:
						lastFire = time.Now()
						pending = false
					default:
					}
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("[watch] error: %v", err)
			}
		}
	}()
}

func matchesExt(path string) bool {
	ext := filepath.Ext(path)
	for _, e := range watchExtensions {
		if ext == e {
			return true
		}
	}
	return false
}
