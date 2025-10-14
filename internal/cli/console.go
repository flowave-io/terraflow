package cli

import (
	"fmt"
	"log"
	"os"

	"github.com/flowave-io/terraflow/internal/monitor"
	"github.com/flowave-io/terraflow/internal/terraform"
)

func RunConsoleCommand(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		printConsoleHelp()
		os.Exit(0)
	}

	refreshCh := make(chan struct{}, 1)
	session := terraform.StartConsoleSession()
	monitor.WatchTerraformFilesNotifying(".", refreshCh)

	for {
		<-refreshCh
		log.Println("Refreshing terraform console due to .tf or .tfvars file change.")
		session.Restart()
	}
}

func printConsoleHelp() {
	fmt.Println("terraflow console: Opens a Terraform console with live automatic updates when .tf/.tfvars files change.\nThis command will listen for Terraform file changes and update variables/resources in-place, seamlessly.\nUsage: terraflow console")
}
