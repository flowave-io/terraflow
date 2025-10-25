package cli

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flowave-io/terraflow/internal/monitor"
	"github.com/flowave-io/terraflow/internal/terraform"
)

func RunConsoleCommand(args []string) {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	pullRemoteState := fs.Bool("pull-remote-state", false, "Pull remote state once and reuse locally in .terraflow/")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	log.Println("Starting terraflow console (TAB completion, history; auto-refresh on .tf/.tfvars)")

	cwd, _ := os.Getwd()
	scratchDir := filepath.Join(cwd, ".terraflow")
	statePath := filepath.Join(scratchDir, "terraform.tfstate")

	// Optional: pull remote state into the scratch state file BEFORE init
	if *pullRemoteState {
		if err := pullRemoteStateOnce(cwd, statePath); err != nil {
			log.Printf("[warn] unable to pull remote state: %v\n", err)
		}
	}

	// Prepare scratch workspace
	if _, _, err := terraform.SyncToScratch(cwd, scratchDir); err != nil {
		log.Printf("[warn] sync to scratch: %v\n", err)
	}
	if err := terraform.InitTerraformInDir(scratchDir); err != nil {
		log.Printf("[warn] terraform init in scratch: %v\n", err)
	}

	// Ensure functions cache exists once
	if err := terraform.EnsureFunctionsCached(scratchDir); err != nil {
		log.Printf("[warn] unable to cache Terraform functions: %v\n", err)
	}

	refreshCh := make(chan struct{}, 1)
	session := terraform.StartConsoleSession(scratchDir, statePath)
	idx, err := terraform.BuildSymbolIndex(scratchDir)
	if err != nil {
		log.Println("[warn] building symbol index:", err)
		idx = &terraform.SymbolIndex{}
	}
	log.Println("Terraform console started.")
	monitor.WatchTerraformFilesNotifying(".", refreshCh)
	RunREPL(session, idx, refreshCh, scratchDir)
}

func printConsoleHelp() {
	fmt.Print(`terraflow console: Live-updating Terraform console

Starts an interactive 'terraform console' that seamlessly updates when .tf/.tfvars files change.
No need to restart manually: edit your Terraform files and context is auto-reloaded for you.

Typical usage:
  terraflow console

Example walkthrough (see test/README.md for test workflow):
  1. Run 'terraflow console' in a directory with your .tf files.
  2. At the prompt, try evaluating variables/expressions.
  3. Edit any .tf or .tfvars file: changes are live—your next expression sees updated context.

Supported files: *.tf, *.tfvars (recursive in subdirs).

For more, see test/fixtures/ and README.md for sample scenarios.
`)
}

// pullRemoteStateOnce ensures the project at workDir is initialized and pulls remote state
// via `terraform state pull`, writing it to statePath. Parent dir is 0700; state file 0600.
func pullRemoteStateOnce(workDir, statePath string) error {
	if workDir == "" {
		wd, _ := os.Getwd()
		workDir = wd
	}
	// Ensure destination directory exists
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	// Initialize the project so backend config is available for state pull
	initCmd := exec.Command("terraform", "init", "-input=false")
	initCmd.Dir = workDir
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	// Pull remote state
	pullCmd := exec.Command("terraform", "state", "pull", "-no-color")
	pullCmd.Dir = workDir
	out, err := pullCmd.Output()
	if err != nil {
		return fmt.Errorf("terraform state pull: %w", err)
	}
	tmp := statePath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := os.Rename(tmp, statePath); err != nil {
		return fmt.Errorf("finalize state: %w", err)
	}
	return nil
}
