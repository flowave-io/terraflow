package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func extractSerialFromMap(t *testing.T, st map[string]any) int {
	t.Helper()
	s, ok := st["serial"]
	if !ok {
		t.Fatalf("serial not found in state")
	}
	switch v := s.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		t.Fatalf("unexpected serial type %T", s)
	}
	return 0
}

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
	if err := PatchStateFromConfig(root, statePath, nil); err != nil {
		t.Fatalf("patch state: %v", err)
	}
	var st1 map[string]any
	if b, err := os.ReadFile(statePath); err != nil {
		t.Fatalf("read state: %v", err)
	} else if err := json.Unmarshal(b, &st1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res1, _ := st1["resources"].([]any)
	if len(res1) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(res1))
	}
	// Verify module scoping: one should be in module.child
	var foundRoot, foundChild bool
	for _, r := range res1 {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		mod, _ := rm["module"].(string)
		name, _ := rm["name"].(string)
		if mod == "" && name == "root_ex" {
			foundRoot = true
		}
		if mod == "module.child" && name == "child_ex" {
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
	serialBefore := extractSerialFromMap(t, st1)
	if err := PatchStateFromConfig(root, statePath, nil); err != nil {
		t.Fatalf("patch state second: %v", err)
	}
	var st2 map[string]any
	if b, err := os.ReadFile(statePath); err != nil {
		t.Fatalf("read state: %v", err)
	} else if err := json.Unmarshal(b, &st2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	serialAfter := extractSerialFromMap(t, st2)
	if serialAfter <= serialBefore {
		t.Fatalf("expected serial to increase, before=%d after=%d", serialBefore, serialAfter)
	}
	// Confirm attribute updated
	var rootRes map[string]any
	res2, _ := st2["resources"].([]any)
	for i := range res2 {
		rm, ok := res2[i].(map[string]any)
		if !ok {
			continue
		}
		mod, _ := rm["module"].(string)
		name, _ := rm["name"].(string)
		if mod == "" && name == "root_ex" {
			rootRes = rm
			break
		}
	}
	if rootRes == nil {
		t.Fatalf("root resource not found or has no instances")
	}
	instRaw, _ := rootRes["instances"].([]any)
	if len(instRaw) == 0 {
		t.Fatalf("root resource has no instances")
	}
	im, _ := instRaw[0].(map[string]any)
	attrs, _ := im["attributes"].(map[string]any)
	if got := attrs["triggers"].(map[string]any)["a"]; got != "z" {
		t.Fatalf("expected updated attribute a=z, got %v", got)
	}
}
