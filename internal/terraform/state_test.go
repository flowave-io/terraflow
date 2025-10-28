package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAndPatchState_AddsAndUpdatesResources(t *testing.T) {
	root := t.TempDir()
	// Root resource and child module
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(`
module "child" { source = "./child" }
resource "null_resource" "root_ex" {
  triggers = { a = "x" }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "child", "main.tf"), []byte(`
resource "null_resource" "child_ex" {
  triggers = { b = "y" }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(root, ".terraflow", "terraform.tfstate")
	if err := EnsureStateInitialized(statePath); err != nil {
		t.Fatalf("init state: %v", err)
	}
	// First patch should add both resources
	if err := PatchStateFromConfig(root, statePath); err != nil {
		t.Fatalf("patch state: %v", err)
	}
	var st1 tfState
	if b, err := os.ReadFile(statePath); err != nil {
		t.Fatalf("read state: %v", err)
	} else if err := json.Unmarshal(b, &st1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(st1.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(st1.Resources))
	}
	// Verify module scoping: one should be in module.child
	var foundRoot, foundChild bool
	for _, r := range st1.Resources {
		if r.Module == "" && r.Name == "root_ex" {
			foundRoot = true
		}
		if r.Module == "module.child" && r.Name == "child_ex" {
			foundChild = true
		}
	}
	if !foundRoot || !foundChild {
		t.Fatalf("did not find expected root/child resources in state")
	}

	// Modify attribute and patch again
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(`
module "child" { source = "./child" }
resource "null_resource" "root_ex" {
  triggers = { a = "z" }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	serialBefore := st1.Serial
	if err := PatchStateFromConfig(root, statePath); err != nil {
		t.Fatalf("patch state second: %v", err)
	}
	var st2 tfState
	if b, err := os.ReadFile(statePath); err != nil {
		t.Fatalf("read state: %v", err)
	} else if err := json.Unmarshal(b, &st2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st2.Serial <= serialBefore {
		t.Fatalf("expected serial to increase, before=%d after=%d", serialBefore, st2.Serial)
	}
	// Confirm attribute updated
	var rootRes *tfStateResource
	for i := range st2.Resources {
		r := &st2.Resources[i]
		if r.Module == "" && r.Name == "root_ex" {
			rootRes = r
			break
		}
	}
	if rootRes == nil || len(rootRes.Instances) == 0 {
		t.Fatalf("root resource not found or has no instances")
	}
	if got := rootRes.Instances[0].Attributes["triggers"].(map[string]any)["a"]; got != "z" {
		t.Fatalf("expected updated attribute a=z, got %v", got)
	}
}
