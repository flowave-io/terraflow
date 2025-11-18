package terraform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	cty "github.com/zclconf/go-cty/cty"
)

// ResourceConfig represents a managed resource discovered in configuration.
type ResourceConfig struct {
	ModulePath []string // module call names in order from root
	Type       string
	Name       string
	Attrs      map[string]any // only literal attributes captured
}

// scanResInfo is used by global-batch evaluation to collect literals and expressions per resource.
type scanResInfo struct {
	modulePath []string
	rType      string
	rName      string
	lit        map[string]any
	exprs      map[string]string
}

// BuildResourceConfigs walks the root module and any nested local modules,
// returning managed resources with literal attributes.
func BuildResourceConfigs(rootDir string) ([]ResourceConfig, error) {
	abs, _ := filepath.Abs(rootDir)
	var out []ResourceConfig

	// Prefer module index (covers registry modules) if available
	if modMap, err := resolveModuleDirs(abs); err == nil && len(modMap) > 0 {
		keys := make([]string, 0, len(modMap))
		for k := range modMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			dir := modMap[k]
			mp := splitModuleKey(k)
			resCfgs, perr := parseModuleResources(dir, mp)
			if perr != nil {
				return out, perr
			}
			out = append(out, resCfgs...)
		}
		return out, nil
	}

	// Fallback: local-only recursion using tfconfig
	visited := map[string]struct{}{}
	var walkModule func(moduleDir string, modulePath []string) error
	walkModule = func(moduleDir string, modulePath []string) error {
		absMod, _ := filepath.Abs(moduleDir)
		if _, ok := visited[absMod]; ok {
			return nil
		}
		visited[absMod] = struct{}{}
		resCfgs, err := parseModuleResources(absMod, modulePath)
		if err != nil {
			return err
		}
		out = append(out, resCfgs...)
		mod, diags := tfconfig.LoadModule(absMod)
		if diags != nil && diags.HasErrors() {
			return fmt.Errorf("%s: %s", absMod, diags.Error())
		}
		if mod == nil {
			return nil
		}
		for name, call := range mod.ModuleCalls {
			if call == nil || strings.TrimSpace(call.Source) == "" {
				continue
			}
			src := call.Source
			if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || filepath.IsAbs(src) {
				next := src
				if !filepath.IsAbs(next) {
					next = filepath.Join(absMod, src)
				}
				if fi, err := os.Stat(next); err == nil && fi.IsDir() {
					if err := walkModule(next, append(append([]string{}, modulePath...), name)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := walkModule(abs, nil); err != nil {
		return out, err
	}
	return out, nil
}

// BuildResourceConfigsEvaluated is like BuildResourceConfigs but attempts to
// evaluate non-literal expressions via terraform console to obtain values.
// workDir/statePath/varFiles should match the console's evaluation context
// (typically the .terraflow scratch directory and its state file).
func BuildResourceConfigsEvaluated(rootDir, workDir, statePath string, varFiles []string) ([]ResourceConfig, error) {
	abs, _ := filepath.Abs(rootDir)
	var out []ResourceConfig
	// simple per-refresh cache: expression source -> evaluated value
	evalCache := map[string]any{}

	if modMap, err := resolveModuleDirs(abs); err == nil && len(modMap) > 0 {
		keys := make([]string, 0, len(modMap))
		for k := range modMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			dir := modMap[k]
			mp := splitModuleKey(k)
			resCfgs, perr := parseModuleResourcesWithEval(dir, mp, workDir, statePath, varFiles, evalCache)
			if perr != nil {
				return out, perr
			}
			out = append(out, resCfgs...)
		}
		return out, nil
	}

	visited := map[string]struct{}{}
	var walkModule func(moduleDir string, modulePath []string) error
	walkModule = func(moduleDir string, modulePath []string) error {
		absMod, _ := filepath.Abs(moduleDir)
		if _, ok := visited[absMod]; ok {
			return nil
		}
		visited[absMod] = struct{}{}
		resCfgs, err := parseModuleResourcesWithEval(absMod, modulePath, workDir, statePath, varFiles, evalCache)
		if err != nil {
			return err
		}
		out = append(out, resCfgs...)
		mod, diags := tfconfig.LoadModule(absMod)
		if diags != nil && diags.HasErrors() {
			return fmt.Errorf("%s: %s", absMod, diags.Error())
		}
		if mod == nil {
			return nil
		}
		for name, call := range mod.ModuleCalls {
			if call == nil || strings.TrimSpace(call.Source) == "" {
				continue
			}
			src := call.Source
			if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || filepath.IsAbs(src) {
				next := src
				if !filepath.IsAbs(next) {
					next = filepath.Join(absMod, src)
				}
				if fi, err := os.Stat(next); err == nil && fi.IsDir() {
					if err := walkModule(next, append(append([]string{}, modulePath...), name)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := walkModule(abs, nil); err != nil {
		return out, err
	}
	return out, nil
}

// BuildResourceConfigsEvaluatedGlobal scans all modules and evaluates all non-literal
// resource attributes in a single batched terraform console invocation for speed.
// Literal attributes are merged with evaluated results.
func BuildResourceConfigsEvaluatedGlobal(rootDir, workDir, statePath string, varFiles []string) ([]ResourceConfig, error) {
	abs, _ := filepath.Abs(rootDir)
	var collected []scanResInfo

	// Walk modules similar to BuildResourceConfigs
	if modMap, err := resolveModuleDirs(abs); err == nil && len(modMap) > 0 {
		keys := make([]string, 0, len(modMap))
		for k := range modMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			dir := modMap[k]
			mp := splitModuleKey(k)
			if err := collectModuleExpressions(dir, mp, &collected); err != nil {
				return nil, err
			}
		}
	} else {
		visited := map[string]struct{}{}
		var walkModule func(moduleDir string, modulePath []string) error
		walkModule = func(moduleDir string, modulePath []string) error {
			absMod, _ := filepath.Abs(moduleDir)
			if _, ok := visited[absMod]; ok {
				return nil
			}
			visited[absMod] = struct{}{}
			if err := collectModuleExpressions(absMod, modulePath, &collected); err != nil {
				return err
			}
			mod, diags := tfconfig.LoadModule(absMod)
			if diags != nil && diags.HasErrors() {
				return fmt.Errorf("%s: %s", absMod, diags.Error())
			}
			if mod == nil {
				return nil
			}
			for name, call := range mod.ModuleCalls {
				if call == nil || strings.TrimSpace(call.Source) == "" {
					continue
				}
				src := call.Source
				if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || filepath.IsAbs(src) {
					next := src
					if !filepath.IsAbs(next) {
						next = filepath.Join(absMod, src)
					}
					if fi, err := os.Stat(next); err == nil && fi.IsDir() {
						if err := walkModule(next, append(append([]string{}, modulePath...), name)); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
		if err := walkModule(abs, nil); err != nil {
			return nil, err
		}
	}

	// Build single batched evaluation as a list of { k = "mod|type.name", v = { ...attrs... } }
	// Using a list avoids invalid HCL object keys (quoted/with dots) in constructors.
	var b strings.Builder
	b.Grow(256 * len(collected))
	b.WriteByte('[')
	firstRes := true
	for _, ri := range collected {
		if len(ri.exprs) == 0 {
			continue
		}
		if !firstRes {
			b.WriteByte(',')
		}
		firstRes = false
		b.WriteString("{ k = \"")
		b.WriteString(modulePathToString(ri.modulePath))
		b.WriteString("|")
		b.WriteString(ri.rType)
		b.WriteByte('.')
		b.WriteString(ri.rName)
		b.WriteString("\", v = {")
		firstAttr := true
		for k, expr := range ri.exprs {
			if !firstAttr {
				b.WriteByte(',')
			}
			firstAttr = false
			b.WriteString(k)
			b.WriteString(" = (")
			b.WriteString(expr)
			b.WriteString(")")
		}
		b.WriteString("} }")
	}
	b.WriteByte(']')

	evaluated := map[string]any{}
	if v, ok := EvalJSON(workDir, statePath, varFiles, b.String(), 3*time.Second); ok {
		if arr, ok := v.([]any); ok {
			for _, it := range arr {
				if m, ok := it.(map[string]any); ok {
					k, _ := m["k"].(string)
					if k == "" {
						continue
					}
					if val, ok := m["v"].(map[string]any); ok {
						evaluated[k] = val
					}
				}
			}
		}
	}

	// Construct results by merging literals with evaluated attrs
	var out []ResourceConfig
	for _, ri := range collected {
		attrs := map[string]any{}
		for k, v := range ri.lit {
			attrs[k] = v
		}
		key := modulePathToString(ri.modulePath) + "|" + ri.rType + "." + ri.rName
		if rm, ok := evaluated[key].(map[string]any); ok {
			for k, v := range rm {
				attrs[k] = v
			}
		}
		out = append(out, ResourceConfig{ModulePath: append([]string{}, ri.modulePath...), Type: ri.rType, Name: ri.rName, Attrs: attrs})
	}
	return out, nil
}

// collectModuleExpressions parses a module directory to collect resources with
// their literal attributes and string forms of non-literal expressions.
func collectModuleExpressions(moduleDir string, modulePath []string, out *[]scanResInfo) error {
	err := filepath.Walk(moduleDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Avoid descending; module recursion handles child dirs
			if p != moduleDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".tf" {
			return nil
		}
		src, f, ok := getSyntaxFileCached(p)
		if !ok || f == nil {
			return nil
		}
		if body, ok := f.Body.(*hclsyntax.Body); ok {
			for _, blk := range body.Blocks {
				if blk == nil || blk.Type != "resource" || len(blk.Labels) < 2 {
					continue
				}
				rType, rName := blk.Labels[0], blk.Labels[1]
				lit := map[string]any{}
				exprs := map[string]string{}
				for k, a := range blk.Body.Attributes {
					if isMetaArg(k) {
						continue
					}
					if v, ok := constValue(a.Expr); ok {
						lit[k] = v
						continue
					}
					r := a.Expr.Range()
					if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
						r = call.Args[0].Range()
					}
					if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
						exprs[k] = string(src[r.Start.Byte:r.End.Byte])
					}
				}
				*out = append(*out, scanResInfo{modulePath: append([]string{}, modulePath...), rType: rType, rName: rName, lit: lit, exprs: exprs})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// -------- Cached HCL parsing to speed up refreshes --------
var (
	parseCacheMu sync.Mutex
	parseCache   = map[string]struct {
		modTime int64
		src     []byte
		file    *hcl.File
	}{}
)

// getSyntaxFileCached returns the file bytes and parsed syntax tree for path, reusing cache when modtime unchanged.
func getSyntaxFileCached(path string) ([]byte, *hcl.File, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil, nil, false
	}
	mt := fi.ModTime().UnixNano()
	parseCacheMu.Lock()
	entry, ok := parseCache[path]
	if ok && entry.modTime == mt && entry.file != nil && len(entry.src) > 0 {
		src := make([]byte, len(entry.src))
		copy(src, entry.src)
		f := entry.file
		parseCacheMu.Unlock()
		return src, f, true
	}
	parseCacheMu.Unlock()
	// Read and parse
	src, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, nil, false
	}
	f, diags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if (diags != nil && diags.HasErrors()) || f == nil {
		return nil, nil, false
	}
	parseCacheMu.Lock()
	parseCache[path] = struct {
		modTime int64
		src     []byte
		file    *hcl.File
	}{modTime: mt, src: src, file: f}
	parseCacheMu.Unlock()
	return src, f, true
}

func parseModuleResourcesWithEval(moduleDir string, modulePath []string, workDir, statePath string, varFiles []string, evalCache map[string]any) ([]ResourceConfig, error) {
	var out []ResourceConfig
	err := filepath.Walk(moduleDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Avoid descending; child modules are handled by recursion
			if p != moduleDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".tf" {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		f, diags := hclsyntax.ParseConfig(src, p, hcl.Pos{Line: 1, Column: 1})
		if diags != nil && diags.HasErrors() || f == nil {
			return nil
		}
		if body, ok := f.Body.(*hclsyntax.Body); ok {
			// Gather per-resource literal attrs and expression attrs
			type resInfo struct {
				rType, rName string
				lit          map[string]any
				exprs        map[string]string
			}
			resources := []resInfo{}
			for _, blk := range body.Blocks {
				if blk == nil || blk.Type != "resource" || len(blk.Labels) < 2 {
					continue
				}
				rType, rName := blk.Labels[0], blk.Labels[1]
				lit := map[string]any{}
				exprs := map[string]string{}
				// Collect attributes for batching
				for k, a := range blk.Body.Attributes {
					if isMetaArg(k) {
						continue
					}
					if v, ok := constValue(a.Expr); ok {
						lit[k] = v
						continue
					}
					r := a.Expr.Range()
					if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
						r = call.Args[0].Range()
					}
					if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
						exprs[k] = string(src[r.Start.Byte:r.End.Byte])
					}
				}
				resources = append(resources, resInfo{rType: rType, rName: rName, lit: lit, exprs: exprs})
			}
			// Build one batch eval for all non-literal expressions in this file
			batched := false
			var result map[string]any
			// Count total exprs
			totalExprs := 0
			for _, ri := range resources {
				totalExprs += len(ri.exprs)
			}
			if totalExprs > 0 {
				var b strings.Builder
				b.Grow(128 * len(resources))
				b.WriteByte('{')
				firstRes := true
				for _, ri := range resources {
					if len(ri.exprs) == 0 {
						continue
					}
					if !firstRes {
						b.WriteByte(',')
					}
					firstRes = false
					// key is "type.name"
					b.WriteByte('"')
					b.WriteString(ri.rType)
					b.WriteByte('.')
					b.WriteString(ri.rName)
					b.WriteByte('"')
					b.WriteString(" = {")
					firstAttr := true
					for k, expr := range ri.exprs {
						if !firstAttr {
							b.WriteByte(',')
						}
						firstAttr = false
						b.WriteString(k)
						b.WriteString(" = (")
						b.WriteString(expr)
						b.WriteString(")")
					}
					b.WriteByte('}')
				}
				b.WriteByte('}')
				if v, ok := EvalJSON(workDir, statePath, varFiles, b.String(), 10*time.Second); ok {
					if mm, ok := v.(map[string]any); ok {
						result = mm
						batched = true
					}
				}
			}
			// Construct output per resource
			for _, ri := range resources {
				attrs := map[string]any{}
				for k, v := range ri.lit {
					attrs[k] = v
				}
				var rm map[string]any
				if batched {
					if m, ok := result[ri.rType+"."+ri.rName].(map[string]any); ok {
						rm = m
					}
				}
				for k, v := range rm {
					attrs[k] = v
				}
				// Fallback per-attribute eval when missing from batch
				for k, expr := range ri.exprs {
					if _, ok := attrs[k]; ok {
						continue
					}
					if v, ok := EvalJSON(workDir, statePath, varFiles, expr, 5*time.Second); ok {
						attrs[k] = v
					}
				}
				out = append(out, ResourceConfig{ModulePath: append([]string{}, modulePath...), Type: ri.rType, Name: ri.rName, Attrs: attrs})
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

// resolveModuleDirs returns mapping from module key ("" for root, "child.grand" for nested) to absolute directory.
func resolveModuleDirs(rootDir string) (map[string]string, error) {
	m := map[string]string{"": rootDir}
	idxPath := filepath.Join(rootDir, ".terraform", "modules", "modules.json")
	b, err := os.ReadFile(idxPath)
	if err != nil {
		return m, err
	}
	var idx struct {
		Modules []struct {
			Key string `json:"Key"`
			Dir string `json:"Dir"`
		} `json:"Modules"`
	}
	if jerr := json.Unmarshal(b, &idx); jerr != nil {
		return m, jerr
	}
	for _, mod := range idx.Modules {
		if strings.TrimSpace(mod.Key) == "" || strings.TrimSpace(mod.Dir) == "" {
			continue
		}
		if !strings.HasPrefix(mod.Key, "root") {
			continue
		}
		key := strings.TrimPrefix(mod.Key, "root")
		key = strings.TrimPrefix(key, ".")
		dir := mod.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(rootDir, dir)
		}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			m[key] = dir
		}
	}
	return m, nil
}

func splitModuleKey(key string) []string {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	parts := strings.Split(key, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseModuleResources(moduleDir string, modulePath []string) ([]ResourceConfig, error) {
	var out []ResourceConfig
	err := filepath.Walk(moduleDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Do not descend into subdirectories here; child modules are handled explicitly by the caller
			if p != moduleDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(p)) != ".tf" {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		f, diags := hclsyntax.ParseConfig(src, p, hcl.Pos{Line: 1, Column: 1})
		if diags != nil && diags.HasErrors() || f == nil {
			return nil
		}
		// Walk resource blocks from syntax tree for reliable nested access
		if body, ok := f.Body.(*hclsyntax.Body); ok {
			for _, blk := range body.Blocks {
				if blk == nil || blk.Type != "resource" || len(blk.Labels) < 2 {
					continue
				}
				rType, rName := blk.Labels[0], blk.Labels[1]
				lit := extractLiteralsFromBody(blk.Body)
				out = append(out, ResourceConfig{ModulePath: append([]string{}, modulePath...), Type: rType, Name: rName, Attrs: lit})
			}
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

func isMetaArg(k string) bool {
	switch k {
	case "provider", "depends_on", "lifecycle", "count", "for_each", "provisioner", "connection":
		return true
	default:
		return false
	}
}

// extractLiteralsFromBody collects literal attributes and nested blocks into a generic map.
// - Attributes: only constant expressions are included
// - Blocks: grouped by type into slices of objects; block labels are injected as name when absent
func extractLiteralsFromBody(body hcl.Body) map[string]any {
	out := map[string]any{}
	if body == nil {
		return out
	}
	// Attributes on this body
	attrs, _ := body.JustAttributes()
	for k, a := range attrs {
		if isMetaArg(k) {
			continue
		}
		if v, ok := constValue(a.Expr); ok {
			out[k] = v
		}
	}
	// Recurse into blocks when we can access syntax nodes
	if syn, ok := body.(*hclsyntax.Body); ok {
		// Group blocks by type and append entries
		groups := map[string][]any{}
		for _, blk := range syn.Blocks {
			if blk == nil || strings.TrimSpace(blk.Type) == "" {
				continue
			}
			if blk.Type == "dynamic" {
				// Skip dynamic blocks; cannot resolve without evaluation
				continue
			}
			m := extractLiteralsFromBody(blk.Body)
			if len(blk.Labels) > 0 {
				if _, exists := m["name"]; !exists {
					m["name"] = blk.Labels[0]
				}
			}
			groups[blk.Type] = append(groups[blk.Type], m)
		}
		for k, v := range groups {
			out[k] = v
		}
	}
	return out
}

// constValue attempts to evaluate an expression purely from literals. If the
// expression references symbols or is not fully known, it returns (nil, false).
func constValue(expr hcl.Expression) (any, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() {
		return nil, false
	}
	if !v.IsWhollyKnown() {
		return nil, false
	}
	return convertCtyToGo(v)
}

func convertCtyToGo(v cty.Value) (any, bool) {
	if !v.IsWhollyKnown() {
		return nil, false
	}
	switch {
	case v.IsNull():
		return nil, true
	case v.Type().IsPrimitiveType():
		switch v.Type() {
		case cty.String:
			return v.AsString(), true
		case cty.Bool:
			if v.RawEquals(cty.True) {
				return true, true
			}
			if v.RawEquals(cty.False) {
				return false, true
			}
			return nil, false
		case cty.Number:
			// best-effort float64 for literals
			f, _ := v.AsBigFloat().Float64()
			return f, true
		}
	case v.Type().IsTupleType() || v.Type().IsListType() || v.Type().IsSetType():
		it := v.ElementIterator()
		var arr []any
		for it.Next() {
			_, ev := it.Element()
			goV, ok := convertCtyToGo(ev)
			if !ok {
				return nil, false
			}
			arr = append(arr, goV)
		}
		return arr, true
	case v.Type().IsMapType() || v.Type().IsObjectType():
		m := map[string]any{}
		for k, ev := range v.AsValueMap() {
			goV, ok := convertCtyToGo(ev)
			if !ok {
				return nil, false
			}
			m[k] = goV
		}
		return m, true
	}
	return nil, false
}
