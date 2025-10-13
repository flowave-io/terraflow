package main

import (
	"fmt"
	"os"
	monitor "github.com/flowave-io/terraflow/internal/monitor"
)

func watchCmd(args []string) {
	path := "."
	if len(args) > 0 {
		path = args[0]
	}
	fmt.Println("Watching .tf files in:", path)
	if err := monitor.WatchTerraformFiles(path); err != nil {
		fmt.Fprintln(os.Stderr, "Watch error:", err)
		os.Exit(2)
	}
}
