package monitor

import (
	"github.com/fsnotify/fsnotify"
	"log"
	"path/filepath"
)

func WatchTerraformFiles(path string) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	err = w.Add(path)
	if err != nil {
		return err
	}

	log.Println("Watching for .tf file changes in", path)
	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return nil
			}
			if event.Op&fsnotify.Write == fsnotify.Write && filepath.Ext(event.Name) == ".tf" {
				log.Println(".tf file changed:", event.Name)
				// TODO: refresh console session
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Println("error:", err)
		}
	}
}
