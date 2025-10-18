package terraform

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SyncToScratch clones Terraform-relevant files from srcDir into scratchDir.
// It copies .tf, .tfvars and .tf.json files, skips .terraform/ and .terraflow/ trees,
// and omits any file that appears to define a backend block. After copying, it writes
// a local backend file to point state to terraform.tfstate in the scratch directory.
func SyncToScratch(srcDir, scratchDir string) error {
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return fmt.Errorf("make scratch: %w", err)
	}
	// Walk source and copy relevant files
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." {
			return nil
		}
		// Skip scratch and terraform dirs
		parts := strings.Split(rel, string(filepath.Separator))
		for _, p := range parts {
			if p == ".terraform" || p == ".terraflow" {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".tf" && ext != ".tfvars" && !(ext == ".json" && strings.HasSuffix(strings.ToLower(path), ".tf.json")) {
			return nil
		}
		// Skip files likely containing backend blocks to avoid conflicts
		if ext == ".tf" {
			if hasBackendBlock(path) {
				return nil
			}
		}
		// Destination path
		dstPath := filepath.Join(scratchDir, rel)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
			return err
		}
		return copyFile(path, dstPath, 0o600)
	})
}

// WriteLocalBackendFile writes a backend.tf file configuring the local backend.
// WriteLocalBackendFile is deprecated for console flow that relies on -state flag.
// Keeping for potential future use; not called by console anymore.
func WriteLocalBackendFile(scratchDir string) error {
	content := "terraform {\n  backend \"local\" {\n    path = \"terraform.tfstate\"\n  }\n}\n"
	backendPath := filepath.Join(scratchDir, "backend.tf")
	return os.WriteFile(backendPath, []byte(content), 0o600)
}

// InitTerraformInDir mirrors the project's .terraform directory into the
// provided directory's .terraform, excluding any terraform.tfstate file.
func InitTerraformInDir(dir string) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir: %w", err)
	}
	src := filepath.Join(workDir, ".terraform")
	info, statErr := os.Stat(src)
	if statErr != nil || !info.IsDir() {
		// Nothing to mirror; treat as no-op
		return nil
	}
	dst := filepath.Join(dir, ".terraform")
	if remErr := os.RemoveAll(dst); remErr != nil {
		return fmt.Errorf("remove existing scratch .terraform: %w", remErr)
	}
	if mkErr := os.MkdirAll(dst, info.Mode()); mkErr != nil {
		return fmt.Errorf("create scratch .terraform: %w", mkErr)
	}
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode())
		}
		// Skip local state file inside .terraform if present
		if filepath.Base(path) == "terraform.tfstate" {
			return nil
		}
		out := filepath.Join(dst, rel)
		if mkDirErr := os.MkdirAll(filepath.Dir(out), info.Mode()); mkDirErr != nil {
			return mkDirErr
		}
		return copyFile(path, out, info.Mode().Perm())
	})
	if walkErr != nil {
		return fmt.Errorf("sync .terraform: %w", walkErr)
	}
	// Ensure state file is not present in destination even if created by other means
	_ = os.Remove(filepath.Join(dst, "terraform.tfstate"))
	// Generate provider lock file once if missing in scratch directory
	lockPath := filepath.Join(dir, ".terraform.lock.hcl")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		cmd := exec.Command("terraform", "providers", "lock", "-fs-mirror", ".terraform/providers")
		cmd.Dir = dir
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("terraform providers lock: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat lock file: %w", err)
	}
	return nil
}

func hasBackendBlock(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if strings.Contains(line, "backend \"") {
			return true
		}
	}
	return false
}

func copyFile(src, dst string, perm os.FileMode) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	tmp := dst + ".tmp"
	d, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, cErr := io.Copy(d, s)
	cErr2 := d.Close()
	if cErr == nil {
		cErr = cErr2
	}
	if cErr != nil {
		_ = os.Remove(tmp)
		return cErr
	}
	return os.Rename(tmp, dst)
}
