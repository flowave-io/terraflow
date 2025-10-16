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
func WriteLocalBackendFile(scratchDir string) error {
	content := "terraform {\n  backend \"local\" {\n    path = \"terraform.tfstate\"\n  }\n}\n"
	backendPath := filepath.Join(scratchDir, "backend.tf")
	return os.WriteFile(backendPath, []byte(content), 0o600)
}

// InitTerraformInDir runs `terraform init -reconfigure` in the given directory.
func InitTerraformInDir(dir string) error {
	cmd := exec.Command("terraform", "init", "-input=false", "-no-color", "-reconfigure")
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
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
