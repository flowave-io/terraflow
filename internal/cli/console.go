package cli

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flowave-io/terraflow/internal/monitor"
	"github.com/flowave-io/terraflow/internal/terraform"
)

// multiStringFlag implements flag.Value allowing repeated -var-file flags
type multiStringFlag []string

func (m *multiStringFlag) String() string {
	if m == nil {
		return ""
	}
	return strings.Join(*m, ",")
}

func (m *multiStringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func RunConsoleCommand(args []string) {
	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: terraflow [global options] console [options]

  Starts an interactive console for experimenting with Terraform
  interpolations.

  This will open an interactive console that you can use to type
  interpolations into and inspect their values. This lets you explore 
  and test interpolations before using them in future configurations.

Options:

  -backend-config=path  Configuration to be merged with what is in the
                        configuration file's 'backend' block. This can be
                        either a path to an HCL file with key/value
                        assignments (same format as terraform.tfvars) or a
                        'key=value' format, and can be specified multiple
                        times. The backend type must be in the configuration
                        itself.

  -pull-remote-state    Pull the state from its location.

  -var-file=path        Set variables in the Terraform configuration from
                        a file. If "terraform.tfvars" or any ".auto.tfvars"
                        files are present, they will be automatically loaded.
`)
	}
	// Support multiple -var-file flags similar to Terraform
	var varFiles multiStringFlag
	fs.Var(&varFiles, "var-file", "Path to a .tfvars file (repeatable). Passed through to terraform console.")
	// Support partial backend configuration like Terraform's -backend-config (repeatable)
	var backendConfigs multiStringFlag
	fs.Var(&backendConfigs, "backend-config", "Partial backend config (KEY=VALUE or file). Repeatable. Triggers terraform init.")
	pullRemoteState := fs.Bool("pull-remote-state", false, "Pull remote state")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}

	log.Println("Starting terraflow console...")

	cwd, _ := os.Getwd()
	scratchDir := filepath.Join(cwd, ".terraflow")
	statePath := filepath.Join(scratchDir, "terraform.tfstate")

	// If any -backend-config is specified, run a full terraform init in the project directory first
	if len(backendConfigs) > 0 && !*pullRemoteState {
		if err := terraform.InitWithBackendConfig(cwd, []string(backendConfigs)); err != nil {
			log.Fatalf("terraform init with backend-config failed: %v", err)
		}
	}

	// Optional: pull remote state into the scratch state file BEFORE init
	if *pullRemoteState {
		if err := pullRemoteStateOnce(cwd, statePath, []string(backendConfigs)); err != nil {
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

	// Normalize var-file paths early (used for startup hydration and session)
	normVarFiles := normalizeVarFiles(scratchDir, []string(varFiles))

	// Ensure local state exists and reflect current config into it before starting console
	if err := terraform.EnsureStateInitialized(statePath); err != nil {
		log.Printf("[warn] ensure local state: %v\n", err)
	} else {
		// Use fast evaluated patch to hydrate non-literals on startup (with normalized var-files)
		if err := terraform.PatchStateFromConfigEvaluatedFast(scratchDir, scratchDir, statePath, normVarFiles); err != nil {
			log.Printf("[warn] patch state from config (evaluated): %v\n", err)
		}
	}

	refreshCh := make(chan struct{}, 1)
	session := terraform.StartConsoleSession(scratchDir, statePath, normVarFiles)
	idx, err := terraform.BuildSymbolIndex(cwd)
	if err != nil {
		log.Println("[warn] building symbol index:", err)
		idx = &terraform.SymbolIndex{}
	}
	log.Println("Terraform console started.")
	monitor.WatchTerraformFilesNotifying(".", refreshCh)
	RunREPL(session, idx, refreshCh, scratchDir, normVarFiles)
}

// pullRemoteStateOnce ensures the project at workDir is initialized and pulls remote state
// via `terraform state pull`, writing it to statePath. Parent dir is 0700; state file 0600.
func pullRemoteStateOnce(workDir, statePath string, backendConfigs []string) error {
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
	if err := terraform.InitWithBackendConfig(workDir, backendConfigs); err != nil {
		return err
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

// normalizeVarFiles returns paths suitable for use when running from scratchDir.
// If a var-file path is absolute, keep as-is. If relative, resolve under scratchDir
// and fall back to the original path if the scratch copy is missing.
func normalizeVarFiles(scratchDir string, vfs []string) []string {
	if len(vfs) == 0 {
		return nil
	}
	out := make([]string, 0, len(vfs))
	for _, vf := range vfs {
		if strings.TrimSpace(vf) == "" {
			continue
		}
		if filepath.IsAbs(vf) {
			out = append(out, vf)
			continue
		}
		// Try scratch path first
		s := filepath.Join(scratchDir, vf)
		if _, err := os.Stat(s); err == nil {
			out = append(out, s)
			continue
		}
		// Fallback to original relative path
		out = append(out, vf)
	}
	return out
}
