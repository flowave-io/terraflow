package terraform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// internal/terraform/indexer_test.go -> repo root is up 3 levels
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func TestBuildSymbolIndex_FixturesBasic(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "test", "fixtures", "basic_console_refresh")
	idx, err := BuildSymbolIndex(dir)
	if err != nil {
		t.Fatalf("BuildSymbolIndex error: %v", err)
	}
	// Expect variable and output from fixture
	if len(idx.Variables) == 0 || idx.Variables[0] != "some_var" {
		t.Fatalf("expected variable some_var, got %#v", idx.Variables)
	}
	if len(idx.Outputs) == 0 || idx.Outputs[0] != "some_var_upper" {
		t.Fatalf("expected output some_var_upper, got %#v", idx.Outputs)
	}
}

func TestCompletionCandidates_Variables(t *testing.T) {
	idx := &SymbolIndex{Variables: []string{"some_var", "other"}}
	line := "var.so"
	cands, start, end := idx.CompletionCandidates(line, len(line))
	if start != 0 || end != len(line) {
		t.Fatalf("unexpected range: %d..%d", start, end)
	}
	if len(cands) == 0 || cands[0] != "var.some_var" {
		t.Fatalf("expected var.some_var suggestion, got %#v", cands)
	}
}

func TestCompletionCandidates_CategoryStarters_EmptyIndex(t *testing.T) {
	idx := &SymbolIndex{
		Variables:  nil,
		Locals:     nil,
		Modules:    nil,
		Resource:   map[string][]string{},
		DataSource: map[string][]string{},
		Outputs:    nil,
	}
	// Try prefixes that would normally suggest starters
	for _, prefix := range []string{"m", "mo", "mod", "v", "l", "d", "o"} {
		cands, _, _ := idx.CompletionCandidates(prefix, len(prefix))
		for _, c := range cands {
			if c == "module." || c == "var." || c == "local." || c == "data." || c == "output." {
				t.Fatalf("unexpected starter %q for empty index with prefix %q: %#v", c, prefix, cands)
			}
		}
	}
}

func TestCompletionCandidates_CategoryStarters_NonEmptyIndex(t *testing.T) {
	idx := &SymbolIndex{
		Variables:  []string{"some_var"},
		Locals:     []string{"x"},
		Modules:    []string{"m1"},
		Resource:   map[string][]string{"aws_s3_bucket": {"b"}},
		DataSource: map[string][]string{"aws_caller_identity": {"current"}},
		Outputs:    []string{"o"},
	}
	// Each prefix should include the matching starter
	cases := map[string]string{
		"v": "var.",
		"l": "local.",
		"m": "module.",
		"d": "data.",
		"o": "output.",
	}
	for prefix, want := range cases {
		cands, _, _ := idx.CompletionCandidates(prefix, len(prefix))
		found := false
		for _, c := range cands {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected starter %q for prefix %q, got %#v", want, prefix, cands)
		}
	}
}
