package terraform

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SyncToScratch incrementally clones Terraform-relevant files from srcDir into scratchDir.
// It copies .tf, .tfvars and .tf.json files, skips .terraform/ and .terraflow/ trees,
// and omits any file that appears to define a backend block. It uses a manifest to
// avoid rewriting unchanged files. It returns whether anything changed and whether
// any .tf files changed (as opposed to only .tfvars/.tf.json changes).
func SyncToScratch(srcDir, scratchDir string) (changed bool, changedTF bool, err error) {
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return false, false, fmt.Errorf("make scratch: %w", err)
	}
	manifestPath := filepath.Join(scratchDir, ".tf-manifest.json")
	oldManifest, _ := readManifest(manifestPath)
	newManifest := map[string]manifestEntry{}

	// Track files seen to identify deletions
	seen := map[string]struct{}{}

	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
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
		isTF := ext == ".tf"
		isTFVars := ext == ".tfvars"
		isTFJSON := ext == ".json" && strings.HasSuffix(strings.ToLower(path), ".tf.json")
		if !isTF && !isTFVars && !isTFJSON {
			return nil
		}
		// Skip files likely containing backend blocks to avoid conflicts
		if isTF && hasBackendBlock(path) {
			return nil
		}
		// Evaluate manifest delta
		relKey := filepath.ToSlash(rel)
		seen[relKey] = struct{}{}
		entry := manifestEntry{ModUnixNano: info.ModTime().UnixNano(), Size: info.Size()}
		newManifest[relKey] = entry
		if prev, ok := oldManifest[relKey]; ok && prev == entry {
			// unchanged
			return nil
		}
		// Copy changed/new file
		dstPath := filepath.Join(scratchDir, rel)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
			return err
		}
		if err := copyFile(path, dstPath, 0o600); err != nil {
			return err
		}
		changed = true
		if isTF {
			changedTF = true
		}
		return nil
	})
	if walkErr != nil {
		return false, false, fmt.Errorf("walk: %w", walkErr)
	}

	// Handle deletions: any file in oldManifest not seen now should be removed
	for rel, prev := range oldManifest {
		if _, ok := seen[rel]; ok {
			continue
		}
		// Only manage our tracked types
		if !strings.HasSuffix(rel, ".tf") && !strings.HasSuffix(rel, ".tfvars") && !strings.HasSuffix(rel, ".tf.json") {
			continue
		}
		// Remove from scratch if exists
		dstPath := filepath.Join(scratchDir, filepath.FromSlash(rel))
		if err := os.Remove(dstPath); err == nil {
			changed = true
			if strings.HasSuffix(rel, ".tf") {
				changedTF = true
			}
		} else if os.IsNotExist(err) {
			// already gone, ignore
		} else if err != nil {
			_ = err // ignore removal errors; leave file in place
		}
		_ = prev
	}

	// Write new manifest atomically
	if err := writeManifest(manifestPath, newManifest); err != nil {
		// Non-fatal to operation, but report error
		return changed, changedTF, fmt.Errorf("write manifest: %w", err)
	}
	return changed, changedTF, nil
}

type manifestEntry struct {
	ModUnixNano int64 `json:"mod_unix_nano"`
	Size        int64 `json:"size"`
}

func readManifest(path string) (map[string]manifestEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]manifestEntry{}, err
	}
	var m map[string]manifestEntry
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]manifestEntry{}, err
	}
	return m, nil
}

func writeManifest(path string, m map[string]manifestEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	// If modules directory is missing, hydrate via a lightweight init to fetch modules only
	modulesDir := filepath.Join(dir, ".terraform", "modules")
	if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
		initCmd := exec.Command("terraform", "init", "-get", "-backend=false", "-input=false", "-no-color")
		initCmd.Dir = dir
		initCmd.Stdout = io.Discard
		initCmd.Stderr = io.Discard
		if err := initCmd.Run(); err != nil {
			return fmt.Errorf("terraform init (modules only): %w", err)
		}
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
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := io.Copy(tmp, s); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, dst)
}

// InitWithBackendConfig runs `terraform init` in workDir, forwarding any provided
// partial backend configuration values as repeated -backend-config flags. Values
// may be KEY=VALUE pairs or paths to *.tfbackend files, matching Terraform's semantics.
func InitWithBackendConfig(workDir string, backendConfigs []string) error {
	args := []string{"init", "-input=false", "-no-color"}
	for _, bc := range backendConfigs {
		bc = strings.TrimSpace(bc)
		if bc == "" {
			continue
		}
		args = append(args, "-backend-config="+bc)
	}
	cmd := exec.Command("terraform", args...)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	return nil
}
