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

	log.Println("Starting terraflow console (TAB completion, history; auto-refresh on .tf/.tfvars)")
	refreshCh := make(chan struct{}, 1)
	session := terraform.StartConsoleSession()
	idx, err := terraform.BuildSymbolIndex(".")
	if err != nil {
		log.Println("[warn] building symbol index:", err)
		idx = &terraform.SymbolIndex{}
	}
	log.Println("Terraform console started.")
	monitor.WatchTerraformFilesNotifying(".", refreshCh)
	RunREPL(session, idx, refreshCh)
}

func printConsoleHelp() {
	fmt.Println(`terraflow console: Live-updating Terraform console\n\nStarts an interactive 'terraform console' that seamlessly updates when .tf/.tfvars files change.\nNo need to restart manually: edit your Terraform files and context is auto-reloaded for you.\n\nTypical usage:\n  terraflow console\n\nExample walkthrough (see test/README.md for test workflow):\n  1. Run 'terraflow console' in a directory with your .tf files.\n  2. At the prompt, try evaluating variables/expressions.\n  3. Edit any .tf or .tfvars file: changes are live—your next expression sees updated context.\n\nSupported files: *.tf, *.tfvars (recursive in subdirs).\n\nFor more, see test/fixtures/ and README.md for sample scenarios.\n`)
}
