package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"os/exec"

	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
)

// SymbolIndex holds discovered Terraform symbols for autocompletion.
type SymbolIndex struct {
	Variables  []string
	Locals     []string
	Modules    []string
	Resource   map[string][]string // type -> names
	DataSource map[string][]string // type -> names
	Outputs    []string
	// Collected attribute keys seen in configuration for each resource/data type
	ResourceAttrs map[string][]string // resource type -> attribute keys (from config)
	DataAttrs     map[string][]string // data type -> attribute keys (from config)
}

// BuildSymbolIndex loads configuration from dir using tfconfig and hcl. It
// follows local child modules and optionally fetches remote (non-registry)
// module sources into a cache under .terraflow/modules.
func BuildSymbolIndex(dir string) (*SymbolIndex, error) {
	idx := &SymbolIndex{
		Resource:      map[string][]string{},
		DataSource:    map[string][]string{},
		ResourceAttrs: map[string][]string{},
		DataAttrs:     map[string][]string{},
	}
	absRoot, _ := filepath.Abs(dir)
	cacheDir := filepath.Join(absRoot, ".terraflow", "modules")
	visited := map[string]struct{}{}

	var allErr error
	if err := indexModuleRecursive(context.Background(), absRoot, absRoot, cacheDir, idx, visited); err != nil {
		allErr = multierror.Append(allErr, err)
	}

	// Optionally hydrate from .terraform/modules if present (covers registry modules)
	modDir := filepath.Join(absRoot, ".terraform", "modules")
	if fi, err := os.Stat(modDir); err == nil && fi.IsDir() {
		_ = filepath.Walk(modDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if _, seen := visited[p]; seen {
				return nil
			}
			_ = indexModuleRecursive(context.Background(), p, p, cacheDir, idx, visited)
			return nil
		})
	}

	// Augment attribute sets with provider schemas if available
	_ = augmentAttributesFromProviderSchemas(dir, idx)

	// Normalize: sort and dedupe
	idx.Variables = uniqueSorted(idx.Variables)
	idx.Locals = uniqueSorted(idx.Locals)
	idx.Modules = uniqueSorted(idx.Modules)
	idx.Outputs = uniqueSorted(idx.Outputs)
	for k, v := range idx.Resource {
		idx.Resource[k] = uniqueSorted(v)
	}
	for k, v := range idx.DataSource {
		idx.DataSource[k] = uniqueSorted(v)
	}
	for k, v := range idx.ResourceAttrs {
		idx.ResourceAttrs[k] = uniqueSorted(v)
	}
	for k, v := range idx.DataAttrs {
		idx.DataAttrs[k] = uniqueSorted(v)
	}

	// Return partial index and a combined error if present
	return idx, allErr
}

func indexModuleRecursive(ctx context.Context, rootDir, moduleDir, cacheDir string, idx *SymbolIndex, visited map[string]struct{}) error {
	abs, _ := filepath.Abs(moduleDir)
	if _, ok := visited[abs]; ok {
		return nil
	}
	visited[abs] = struct{}{}

	mod, diags := tfconfig.LoadModule(abs)
	var resultErr error
	if diags != nil && diags.HasErrors() {
		resultErr = multierror.Append(resultErr, fmt.Errorf("%s: %s", abs, diags.Error()))
	}
	if mod == nil {
		return resultErr
	}

	// Variables
	for name := range mod.Variables {
		idx.Variables = append(idx.Variables, name)
	}
	// Outputs
	for name := range mod.Outputs {
		idx.Outputs = append(idx.Outputs, name)
	}
	// Resources
	for _, r := range mod.ManagedResources {
		if r == nil || r.Type == "" || r.Name == "" {
			continue
		}
		idx.Resource[r.Type] = append(idx.Resource[r.Type], r.Name)
	}
	// Data sources
	for _, d := range mod.DataResources {
		if d == nil || d.Type == "" || d.Name == "" {
			continue
		}
		idx.DataSource[d.Type] = append(idx.DataSource[d.Type], d.Name)
	}
	// Lightweight attribute keys collection from HCL AST (best-effort):
	// We scan *.tf files for blocks of form resource "type" "name" { attr = ... }
	// and collect top-level attribute keys appearing under that type. Same for data.
	// This does not validate the provider schema; it's purely heuristic from config.
	_ = filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".tf" {
			return nil
		}
		f, diags := hclparse.NewParser().ParseHCLFile(p)
		if diags != nil && diags.HasErrors() || f == nil {
			return nil
		}
		schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource"}, {Type: "data"}}}
		content, _, _ := f.Body.PartialContent(schema)
		for _, b := range content.Blocks {
			switch b.Type {
			case "resource":
				if len(b.Labels) < 2 {
					continue
				}
				rType := b.Labels[0]
				attrs, _ := b.Body.JustAttributes()
				for k := range attrs {
					idx.ResourceAttrs[rType] = append(idx.ResourceAttrs[rType], k)
				}
			case "data":
				if len(b.Labels) < 2 {
					continue
				}
				dType := b.Labels[0]
				attrs, _ := b.Body.JustAttributes()
				for k := range attrs {
					idx.DataAttrs[dType] = append(idx.DataAttrs[dType], k)
				}
			}
		}
		return nil
	})
	// Locals via HCL parse
	locals, lerr := parseLocals(abs)
	if lerr != nil {
		resultErr = multierror.Append(resultErr, lerr)
	}
	idx.Locals = append(idx.Locals, locals...)

	// Modules
	for name, call := range mod.ModuleCalls {
		if name != "" {
			idx.Modules = append(idx.Modules, name)
		}
		if call == nil || strings.TrimSpace(call.Source) == "" {
			continue
		}
		src := call.Source
		// Local path
		if isLikelyLocalPath(src) {
			child := src
			if !filepath.IsAbs(child) {
				child = filepath.Join(abs, child)
			}
			_ = indexModuleRecursive(ctx, rootDir, child, cacheDir, idx, visited)
			continue
		}
		// Registry addresses are handled via .terraform/modules hydration
		if isRegistryAddress(src) {
			continue
		}
		// Remote via go-getter
		if local, err := ResolveOrFetchModuleSource(ctx, src, cacheDir); err == nil && local != "" {
			_ = indexModuleRecursive(ctx, rootDir, local, cacheDir, idx, visited)
		} else if err != nil {
			resultErr = multierror.Append(resultErr, fmt.Errorf("module %q: %v", name, err))
		}
	}
	return resultErr
}

func parseLocals(dir string) ([]string, error) {
	parser := hclparse.NewParser()
	var out []string
	var allErr error
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			// skip heavy/internal dirs
			if info != nil && info.IsDir() {
				base := filepath.Base(p)
				if base == ".terraform" || base == ".terraflow" || strings.HasPrefix(base, ".git") || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".tf" {
			return nil
		}
		f, diags := parser.ParseHCLFile(p)
		if diags != nil && diags.HasErrors() {
			allErr = multierror.Append(allErr, fmt.Errorf("%s: %s", p, diags.Error()))
			return nil
		}
		if f == nil {
			return nil
		}
		schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "locals"}}}
		content, _, _ := f.Body.PartialContent(schema)
		for _, b := range content.Blocks {
			if b.Type != "locals" {
				continue
			}
			attrs, _ := b.Body.JustAttributes()
			for name := range attrs {
				out = append(out, name)
			}
		}
		return nil
	})
	if err != nil {
		allErr = multierror.Append(allErr, err)
	}
	return out, allErr
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// augmentAttributesFromProviderSchemas tries to query terraform providers for schema
// via `terraform providers schema -json` and enrich resource/data attribute lists.
// If the command fails, this function is a no-op.
func augmentAttributesFromProviderSchemas(dir string, idx *SymbolIndex) error {
	bin := "terraform"
	cmd := exec.Command(bin, "providers", "schema", "-json")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return err
	}
	// Minimal struct to parse resources and datasources
	var doc struct {
		ProviderSchemas map[string]struct {
			ResourceSchemas map[string]struct {
				Block struct {
					Attributes map[string]any `json:"attributes"`
				} `json:"block"`
			} `json:"resource_schemas"`
			DataSourceSchemas map[string]struct {
				Block struct {
					Attributes map[string]any `json:"attributes"`
				} `json:"block"`
			} `json:"data_source_schemas"`
		} `json:"provider_schemas"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return err
	}
	// The keys for resources are provider-qualified like "azurerm_resource_group" in TF 1.6+ (depends).
	// We'll merge by suffix matching against types we already know.
	// Fill maps of type->attrs from provider schemas.
	for _, prov := range doc.ProviderSchemas {
		for rType, rSchema := range prov.ResourceSchemas {
			// prefer exact key; otherwise allow suffix after last '.'
			t := rType
			if i := strings.LastIndex(t, "."); i >= 0 {
				t = t[i+1:]
			}
			if len(rSchema.Block.Attributes) > 0 {
				for k := range rSchema.Block.Attributes {
					idx.ResourceAttrs[t] = append(idx.ResourceAttrs[t], k)
				}
			}
		}
		for dType, dSchema := range prov.DataSourceSchemas {
			t := dType
			if i := strings.LastIndex(t, "."); i >= 0 {
				t = t[i+1:]
			}
			if len(dSchema.Block.Attributes) > 0 {
				for k := range dSchema.Block.Attributes {
					idx.DataAttrs[t] = append(idx.DataAttrs[t], k)
				}
			}
		}
	}
	return nil
}

// CompletionCandidates generates suggestions for a given tokenized context.
// cursorIndex is byte index in line. Returns suggestions and the range [start,end)
// (byte offsets) of the token to replace.
func (s *SymbolIndex) CompletionCandidates(line string, cursorIndex int) (candidates []string, start int, end int) {
	if cursorIndex < 0 || cursorIndex > len(line) {
		cursorIndex = len(line)
	}
	// Find token boundaries: identifiers, dots, underscores and slashes/hyphens in types
	isTokChar := func(r rune) bool {
		if r == '.' || r == '_' || r == '-' {
			return true
		}
		return r == ':' || r == '/' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	// Walk backward to token start
	start = cursorIndex
	for start > 0 {
		r, size := rune(line[start-1]), 1
		if (start-1) >= 0 && (start-1) < len(line) {
			r = rune(line[start-1])
			size = 1
		}
		if !isTokChar(r) {
			break
		}
		start -= size
	}
	// Walk forward to token end
	end = cursorIndex
	for end < len(line) {
		r := rune(line[end])
		if !isTokChar(r) {
			break
		}
		end++
	}
	token := strings.TrimSpace(line[start:end])
	lower := strings.ToLower(token)

	// Friendly handling: allow bare keywords without trailing dot to behave like prefix with dot
	switch lower {
	case "var":
		token, lower = "var.", "var."
	case "local":
		token, lower = "local.", "local."
	case "module":
		token, lower = "module.", "module."
	case "output":
		token, lower = "output.", "output."
	case "data":
		token, lower = "data.", "data."
	}

	// Patterns: var., local., module., data., output., <type>., data.<type>., type.name.
	switch {
	case strings.HasPrefix(lower, "var."):
		prefix := token[len("var."):]
		for _, v := range s.Variables {
			if strings.HasPrefix(v, prefix) {
				candidates = append(candidates, "var."+v)
			}
		}
	case strings.HasPrefix(lower, "local."):
		prefix := token[len("local."):]
		for _, v := range s.Locals {
			if strings.HasPrefix(v, prefix) {
				candidates = append(candidates, "local."+v)
			}
		}
	case strings.HasPrefix(lower, "module."):
		prefix := token[len("module."):]
		for _, v := range s.Modules {
			if strings.HasPrefix(v, prefix) {
				candidates = append(candidates, "module."+v)
			}
		}
	case strings.HasPrefix(lower, "output."):
		prefix := token[len("output."):]
		for _, v := range s.Outputs {
			if strings.HasPrefix(v, prefix) {
				candidates = append(candidates, "output."+v)
			}
		}
	case strings.HasPrefix(lower, "data."):
		rest := token[len("data."):]
		// Two-level completion for data: type[.name]
		if i := strings.Index(rest, "."); i == -1 {
			// Complete data types
			for dType := range s.DataSource {
				if strings.HasPrefix(dType, rest) {
					candidates = append(candidates, "data."+dType)
				}
			}
		} else {
			dType := rest[:i]
			namePrefix := rest[i+1:]
			if names, ok := s.DataSource[dType]; ok {
				for _, n := range names {
					if strings.HasPrefix(n, namePrefix) {
						candidates = append(candidates, "data."+dType+"."+n)
					}
				}
			}
		}
	default:
		// Resource completion: <type>[.name[.attr]]
		if i := strings.Index(token, "."); i == -1 {
			// Completing a top-level symbol: resource type OR category keywords (var/local/module/data/output)
			for rType := range s.Resource {
				if strings.HasPrefix(rType, token) {
					candidates = append(candidates, rType)
				}
			}
			// Also propose category starters if they match the current prefix (case-insensitive)
			kwPrefix := strings.ToLower(token)
			for _, kw := range []string{"var.", "local.", "module.", "data.", "output."} {
				if strings.HasPrefix(kw, kwPrefix) {
					candidates = append(candidates, kw)
				}
			}
		} else {
			rest := token
			parts := strings.Split(rest, ".")
			if len(parts) == 2 {
				// <type>.<name-prefix>
				rType := parts[0]
				namePrefix := parts[1]
				if names, ok := s.Resource[rType]; ok {
					for _, n := range names {
						if strings.HasPrefix(n, namePrefix) {
							candidates = append(candidates, rType+"."+n)
						}
					}
				}
			} else if len(parts) >= 3 {
				// <type>.<name>.<attr-prefix>
				rType := parts[0]
				attrPrefix := parts[2]
				if attrs, ok := s.ResourceAttrs[rType]; ok {
					for _, a := range attrs {
						if strings.HasPrefix(a, attrPrefix) {
							candidates = append(candidates, rType+"."+parts[1]+"."+a)
						}
					}
				}
			}
		}
	}

	sort.Strings(candidates)
	return candidates, start, end
}
