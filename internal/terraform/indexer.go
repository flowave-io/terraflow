package terraform

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SymbolIndex holds discovered Terraform symbols for autocompletion.
type SymbolIndex struct {
	Variables  []string
	Locals     []string
	Modules    []string
	Resource   map[string][]string // type -> names
	DataSource map[string][]string // type -> names
	Outputs    []string
}

// BuildSymbolIndex walks the directory tree rooted at dir and extracts symbols
// from .tf and .tfvars files using lightweight regex-based scanning.
func BuildSymbolIndex(dir string) (*SymbolIndex, error) {
	idx := &SymbolIndex{
		Resource:   map[string][]string{},
		DataSource: map[string][]string{},
	}

	// Regex patterns for HCL blocks we care about
	// Examples matched:
	//   variable "name" { ... }
	//   resource "aws_s3_bucket" "app" { ... }
	//   data "aws_iam_policy" "readonly" { ... }
	//   module "network" { ... }
	//   output "url" { ... }
	//   locals { name = "value" other = 1 }
	reVariable := regexp.MustCompile(`^\s*variable\s+"([^"]+)"`)
	reResource := regexp.MustCompile(`^\s*resource\s+"([^"]+)"\s+"([^"]+)"`)
	reData := regexp.MustCompile(`^\s*data\s+"([^"]+)"\s+"([^"]+)"`)
	reModule := regexp.MustCompile(`^\s*module\s+"([^"]+)"`)
	reOutput := regexp.MustCompile(`^\s*output\s+"([^"]+)"`)
	reLocalsStart := regexp.MustCompile(`^\s*locals\s*{`)
	reLocalEntry := regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=`)

	// Track whether we are inside a locals { } block and its brace depth
	type localsState struct {
		inBlock bool
		depth   int
	}
	var locState localsState

	root := filepath.Clean(dir)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// skip hidden/vendor-like directories EXCEPT the root itself
			if filepath.Clean(path) == root {
				return nil
			}
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" || base == ".git" || base == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".tf" && ext != ".tfvars" {
			return nil
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			// quick comment strip
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}

			if reLocalsStart.MatchString(line) {
				locState.inBlock = true
				locState.depth = 1
				continue
			}
			if locState.inBlock {
				// Count braces to know when locals block ends
				locState.depth += strings.Count(line, "{")
				locState.depth -= strings.Count(line, "}")
				if m := reLocalEntry.FindStringSubmatch(line); m != nil {
					name := m[1]
					if name != "" {
						idx.Locals = append(idx.Locals, name)
					}
				}
				if locState.depth <= 0 {
					locState = localsState{}
				}
				continue
			}

			if m := reVariable.FindStringSubmatch(line); m != nil {
				idx.Variables = append(idx.Variables, m[1])
				continue
			}
			if m := reResource.FindStringSubmatch(line); m != nil {
				rType, rName := m[1], m[2]
				idx.Resource[rType] = append(idx.Resource[rType], rName)
				continue
			}
			if m := reData.FindStringSubmatch(line); m != nil {
				dType, dName := m[1], m[2]
				idx.DataSource[dType] = append(idx.DataSource[dType], dName)
				continue
			}
			if m := reModule.FindStringSubmatch(line); m != nil {
				idx.Modules = append(idx.Modules, m[1])
				continue
			}
			if m := reOutput.FindStringSubmatch(line); m != nil {
				idx.Outputs = append(idx.Outputs, m[1])
				continue
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk: %w", walkErr)
	}

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
	return idx, nil
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

	// Patterns: var., local., module., data., output., <type>., data.<type>.
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
		// Resource completion: <type>[.name]
		if i := strings.Index(token, "."); i == -1 {
			// Completing a resource type
			for rType := range s.Resource {
				if strings.HasPrefix(rType, token) {
					candidates = append(candidates, rType)
				}
			}
		} else {
			rType := token[:i]
			namePrefix := token[i+1:]
			if names, ok := s.Resource[rType]; ok {
				for _, n := range names {
					if strings.HasPrefix(n, namePrefix) {
						candidates = append(candidates, rType+"."+n)
					}
				}
			}
		}
	}

	sort.Strings(candidates)
	return candidates, start, end
}
