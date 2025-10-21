package terraform

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/go-getter"
	"github.com/hashicorp/go-safetemp"
)

// ResolveOrFetchModuleSource returns a local filesystem path for a module source.
// - Local paths are returned as absolute paths.
// - Registry addresses (e.g., registry.terraform.io/... or short source) are returned empty (caller may rely on .terraform/modules).
// - URL/VCS sources are downloaded into cacheDir and the local path is returned.
func ResolveOrFetchModuleSource(ctx context.Context, source string, cacheDir string) (string, error) {
	s := strings.TrimSpace(source)
	if s == "" {
		return "", fmt.Errorf("empty module source")
	}
	// Local path (relative or absolute)
	if isLikelyLocalPath(s) {
		abs, err := filepath.Abs(s)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			return abs, nil
		}
		return "", fmt.Errorf("local module path not found: %s", abs)
	}
	// Registry style sources: return empty and let caller use .terraform/modules if present
	if isRegistryAddress(s) {
		return "", nil
	}
	// URL/VCS via go-getter
	if cacheDir == "" {
		return "", fmt.Errorf("cacheDir required for remote sources")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	dest := filepath.Join(cacheDir, fingerprint(s))
	if fi, err := os.Stat(dest); err == nil && fi.IsDir() {
		return dest, nil
	}
	// Create a temporary directory within cacheDir to allow atomic rename
	tmpDir, cleanup, err := safetemp.Dir(cacheDir, "modfetch-")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = cleanup.Close() }()

	client := &getter.Client{
		Ctx:  ctx,
		Src:  s,
		Dst:  tmpDir,
		Mode: getter.ClientModeAny,
		// Ensure standard HTTP behaviors (proxies, certs, etc.)
		Getters: map[string]getter.Getter{
			"http":  &getter.HttpGetter{Netrc: true, Client: defaultHTTPClient()},
			"https": &getter.HttpGetter{Netrc: true, Client: defaultHTTPClient()},
			"git":   &getter.GitGetter{},
			"file":  &getter.FileGetter{},
		},
	}
	if err := client.Get(); err != nil {
		return "", fmt.Errorf("fetch module source: %w", err)
	}
	// Move into deterministic cache path
	if err := os.Rename(tmpDir, dest); err != nil {
		return "", fmt.Errorf("cache move: %w", err)
	}
	// Prevent cleanup from removing the renamed directory
	cleanup = nil
	return dest, nil
}

func defaultHTTPClient() *http.Client {
	return cleanhttp.DefaultClient()
}

func fingerprint(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func isLikelyLocalPath(s string) bool {
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/") {
		return true
	}
	// Windows drive letters or backslashes are intentionally ignored for portability
	if strings.HasPrefix(s, "file://") {
		return true
	}
	return false
}

func isRegistryAddress(s string) bool {
	if strings.HasPrefix(s, "registry.terraform.io/") {
		return true
	}
	// Common shorthand source forms (namespace/name/provider)
	if !strings.Contains(s, "://") && strings.Count(s, "/") == 2 {
		return true
	}
	return false
}
