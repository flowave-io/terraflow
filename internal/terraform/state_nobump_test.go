package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchState_NoSerialBumpWhenNoChanges(t *testing.T) {
	root := t.TempDir()
	// minimal resource with literal attribute
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(`
resource "null_resource" "ex" {
  triggers = { a = "x" }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, ".terraflow", "terraform.tfstate")
	if err := EnsureStateInitialized(statePath); err != nil {
		t.Fatalf("init state: %v", err)
	}
	if err := PatchStateFromConfig(root, statePath, nil); err != nil {
		t.Fatalf("first patch: %v", err)
	}
	// Read serial after first patch
	b1, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st1 map[string]any
	if err := json.Unmarshal(b1, &st1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	serial1 := extractSerial(t, st1)
	// Second patch with no changes should not bump serial
	if err := PatchStateFromConfig(root, statePath, nil); err != nil {
		t.Fatalf("second patch: %v", err)
	}
	b2, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state 2: %v", err)
	}
	var st2 map[string]any
	if err := json.Unmarshal(b2, &st2); err != nil {
		t.Fatalf("unmarshal 2: %v", err)
	}
	serial2 := extractSerial(t, st2)
	if serial2 != serial1 {
		// If serial changed, we unnecessarily rewrote the state
		t.Fatalf("expected serial unchanged, got %d -> %d", serial1, serial2)
	}
}

func extractSerial(t *testing.T, st map[string]any) int {
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
