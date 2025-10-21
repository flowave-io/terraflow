package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/flowave-io/terraflow/internal/cli"
	"github.com/flowave-io/terraflow/internal/terraform"
)

const version = "0.1.0"

func printHelp() {
	fmt.Print(`Terraflow: Real-time development tool for Terraform and OpenTofu

Usage:
  terraflow [command] [options]

Available Commands:
  help       Show this help message and exit
  version    Show version information and exit
`)
}

func main() {
	flag.Usage = printHelp
	flagHelp := flag.Bool("help", false, "Show help")
	flag.Parse()

	args := flag.Args()

	if *flagHelp || len(args) == 0 || args[0] == "help" {
		printHelp()
		os.Exit(0)
	}

	if args[0] == "version" {
		fmt.Println("terraflow version", version)
		os.Exit(0)
	}

	if args[0] == "console" {
		// Warn-only Terraform version check before starting console
		terraform.CheckVersionWarn()
		// defer to the CLI console handler
		cli.RunConsoleCommand(args[1:])
		os.Exit(0)
	}

	fmt.Fprintln(os.Stderr, "Unknown command: ", args[0])
	printHelp()
	os.Exit(1)
}
