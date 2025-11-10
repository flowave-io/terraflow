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
	fmt.Print(`Terraflow is a real-time development solution for Terraform and OpenTofu.

Usage: terraflow [global options] <subcommand> [args]

Available commands:
  help     Show this help output, or the help for a specified subcommand
  version  Show the current Terraflow version
  console  Try Terraform expressions at an interactive command prompt
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
		fmt.Println("Terraflow", version)
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
