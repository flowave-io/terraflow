package terraform

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// EnsureFunctionsCached guarantees a cached JSON of Terraform functions exists under
// the given scratchDir (e.g., .terraflow). If missing, it fetches the list from
// HashiCorp docs and writes it as a JSON array of strings (0600).
func EnsureFunctionsCached(scratchDir string) error {
	if strings.TrimSpace(scratchDir) == "" {
		return errors.New("scratchDir is empty")
	}
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return err
	}
	cachePath := filepath.Join(scratchDir, "functions.json")
	if fi, err := os.Stat(cachePath); err == nil && !fi.IsDir() {
		return nil
	}
	names, err := fetchTerraformFunctionNames()
	if err != nil {
		return err
	}
	b, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath, b, 0o600)
}

// LoadTerraformFunctions reads the cached functions list from `.terraflow/functions.json`.
// Returns an empty slice if the file is missing or malformed.
func LoadTerraformFunctions(scratchDir string) []string {
	cachePath := filepath.Join(scratchDir, "functions.json")
	b, err := os.ReadFile(cachePath)
	if err != nil || len(b) == 0 {
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return nil
	}
	// normalize, unique, sorted
	seen := map[string]struct{}{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(strings.ToLower(n))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// fetchTerraformFunctionNames gets the function list page and extracts unique function names.
func fetchTerraformFunctionNames() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://developer.hashicorp.com/terraform/language/functions", nil)
	if err != nil {
		return nil, err
	}
	// Basic headers to be nice
	req.Header.Set("User-Agent", "terraflow/console")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("unexpected status fetching functions")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Extract `/terraform/language/functions/<name>`
	re := regexp.MustCompile(`/terraform/language/functions/([a-z0-9_]+)`) // greedy enough
	m := re.FindAllStringSubmatch(string(body), -1)
	if len(m) == 0 {
		return nil, errors.New("no function names found")
	}
	seen := map[string]struct{}{}
	var out []string
	for _, g := range m {
		if len(g) < 2 {
			continue
		}
		n := strings.TrimSpace(strings.ToLower(g[1]))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
