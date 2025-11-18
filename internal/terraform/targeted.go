package terraform

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	cty "github.com/zclconf/go-cty/cty"
)

type attrExpr struct {
	ModulePath  []string
	Type        string
	Name        string
	Attr        string
	Expr        string
	IsLiteral   bool
	LitValue    any
	CountExpr   string
	ForEachExpr string
}

// collectResourceAttrExpressions scans modules and returns expressions/literals for a specific resource attribute.
func collectResourceAttrExpressions(rootDir, rType, attr string) ([]attrExpr, error) {
	abs, _ := filepath.Abs(rootDir)
	var out []attrExpr
	if modMap, err := resolveModuleDirs(abs); err == nil && len(modMap) > 0 {
		keys := make([]string, 0, len(modMap))
		for k := range modMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			dir := modMap[k]
			mp := splitModuleKey(k)
			if err := collectInDirForType(dir, mp, rType, attr, &out); err != nil {
				return out, err
			}
		}
		return out, nil
	}
	// Fallback: root-only walk
	if err := collectInDirForType(abs, nil, rType, attr, &out); err != nil {
		return out, err
	}
	return out, nil
}

func collectInDirForType(dir string, modulePath []string, rType, attr string, out *[]attrExpr) error {
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := filepath.Base(p)
				if (base == ".terraform" || base == ".terraflow" || strings.HasPrefix(base, ".git")) && p != dir {
					return filepath.SkipDir
				}
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
				brType, brName := blk.Labels[0], blk.Labels[1]
				if brType != rType {
					continue
				}
				var ae attrExpr
				ae.ModulePath = append([]string{}, modulePath...)
				ae.Type = brType
				ae.Name = brName
				ae.Attr = attr
				// capture meta
				if a, ok := blk.Body.Attributes["count"]; ok && a != nil {
					r := a.Expr.Range()
					if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
						ae.CountExpr = string(src[r.Start.Byte:r.End.Byte])
					}
				}
				if a, ok := blk.Body.Attributes["for_each"]; ok && a != nil {
					r := a.Expr.Range()
					if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
						ae.ForEachExpr = string(src[r.Start.Byte:r.End.Byte])
					}
				}
				// target attribute
				if a, ok := blk.Body.Attributes[attr]; ok && a != nil {
					if v, okc := constValue(a.Expr); okc {
						ae.IsLiteral = true
						ae.LitValue = v
					} else {
						r := a.Expr.Range()
						if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
							r = call.Args[0].Range()
						}
						if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
							ae.Expr = string(src[r.Start.Byte:r.End.Byte])
						}
					}
					*out = append(*out, ae)
				}
			}
		}
		return nil
	})
}

// PatchSpecificResourceAttr evaluates and patches a single attribute for a resource type.
func PatchSpecificResourceAttr(rootDir, workDir, statePath string, varFiles []string, rType, attr string) error {
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(statePath) == "" {
		return fmt.Errorf("rootDir/statePath required")
	}
	if err := EnsureStateInitialized(statePath); err != nil {
		return err
	}
	list, err := collectResourceAttrExpressions(rootDir, rType, attr)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}
	type resRef struct {
		idx int
		obj map[string]any
	}
	index := map[string]resRef{}
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			if mode, _ := m["mode"].(string); mode != "managed" {
				continue
			}
			rt, _ := m["type"].(string)
			name, _ := m["name"].(string)
			mod, _ := m["module"].(string)
			index[resourceKey(mod, rt, name)] = resRef{idx: i, obj: m}
		}
	}
	changed := false
	for _, it := range list {
		key := resourceKey(modulePathToString(it.ModulePath), it.Type, it.Name)
		ref, ok := index[key]
		if !ok {
			continue
		}
		instRaw, _ := ref.obj["instances"].([]any)
		if instRaw == nil {
			instRaw = []any{}
		}
		var val any
		if it.IsLiteral {
			val = it.LitValue
		} else if strings.TrimSpace(it.Expr) != "" {
			if v, ok := TryEvalInProcess(workDir, varFiles, it.Expr, 1*time.Second); ok {
				val = v
			} else if v, ok := EvalJSON(workDir, statePath, varFiles, it.Expr, 3*time.Second); ok {
				val = v
			}
		}
		if val == nil {
			continue
		}
		if len(instRaw) == 0 {
			inst := map[string]any{"attributes": map[string]any{it.Attr: sanitizeValue(val)}, "schema_version": 0}
			ref.obj["instances"] = []any{inst}
			resources[ref.idx] = ref.obj
			changed = true
			continue
		}
		for j := range instRaw {
			im, ok := instRaw[j].(map[string]any)
			if !ok {
				continue
			}
			attrs, _ := im["attributes"].(map[string]any)
			if attrs == nil {
				attrs = map[string]any{}
				im["attributes"] = attrs
			}
			nv := sanitizeValue(val)
			ov, exists := attrs[it.Attr]
			if !exists || !deepEqualJSONish(ov, nv) {
				attrs[it.Attr] = nv
				changed = true
			}
		}
		ref.obj["instances"] = instRaw
		resources[ref.idx] = ref.obj
	}
	if !changed {
		return nil
	}
	st["resources"] = resources
	return writeStateBump(statePath, st, b)
}

// PatchTargetedByFiles evaluates and patches only resources/attributes present in the given .tf files.
func PatchTargetedByFiles(rootDir, workDir, statePath string, varFiles []string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	if err := EnsureStateInitialized(statePath); err != nil {
		return err
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}
	type resRef struct {
		idx int
		obj map[string]any
	}
	index := map[string]resRef{}
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			if mode, _ := m["mode"].(string); mode != "managed" {
				continue
			}
			rt, _ := m["type"].(string)
			name, _ := m["name"].(string)
			mod, _ := m["module"].(string)
			index[resourceKey(mod, rt, name)] = resRef{idx: i, obj: m}
		}
	}
	// Walk provided files
	changed := false
	for _, p := range files {
		src, f, ok := getSyntaxFileCached(p)
		if !ok || f == nil {
			continue
		}
		if body, ok := f.Body.(*hclsyntax.Body); ok {
			for _, blk := range body.Blocks {
				if blk == nil || blk.Type != "resource" || len(blk.Labels) < 2 {
					continue
				}
				rType, rName := blk.Labels[0], blk.Labels[1]
				modKey := resourceKey("", rType, rName)
				// locate actual key including module path by scanning existing index too
				// try plain first
				ref, ok := index[modKey]
				if !ok {
					// attempt to find any module-scoped entry with same type/name (first match)
					for k, v := range index {
						if strings.HasSuffix(k, "|"+rType+"|"+rName) || strings.HasSuffix(k, rType+"|"+rName) {
							ref = v
							ok = true
							break
						}
					}
				}
				// If not present, create new minimal managed entry
				if !ok {
					newRes := map[string]any{
						"mode":     "managed",
						"type":     rType,
						"name":     rName,
						"provider": providerAddressForType(rType),
						"instances": []any{map[string]any{
							"attributes":     map[string]any{},
							"schema_version": 0,
						}},
					}
					resources = append(resources, newRes)
					ref = resRef{idx: len(resources) - 1, obj: newRes}
					index[modKey] = ref
					changed = true
				} else {
					if _, hasProv := ref.obj["provider"]; !hasProv {
						ref.obj["provider"] = providerAddressForType(rType)
					}
				}
				// eval attributes (batch unresolved into one terraform console call)
				// 1) Gather literal or in-process values
				resolved := map[string]any{}
				unresolved := map[string]string{}
				for k, a := range blk.Body.Attributes {
					if isMetaArg(k) {
						continue
					}
					if v, ok := constValue(a.Expr); ok {
						resolved[k] = v
						continue
					}
					r := a.Expr.Range()
					if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
						r = call.Args[0].Range()
					}
					if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
						expr := string(src[r.Start.Byte:r.End.Byte])
						if v, ok := TryEvalInProcess(workDir, varFiles, expr, 50*time.Millisecond); ok {
							resolved[k] = v
						} else {
							unresolved[k] = expr
						}
					}
				}
				// 2) Batch console-evaluate unresolved attributes once
				if len(unresolved) > 0 {
					var bb strings.Builder
					bb.Grow(64 * len(unresolved))
					bb.WriteByte('{')
					first := true
					for k, expr := range unresolved {
						if !first {
							bb.WriteByte(',')
						} else {
							first = false
						}
						bb.WriteString(k)
						bb.WriteString(" = (")
						bb.WriteString(expr)
						bb.WriteString(")")
					}
					bb.WriteByte('}')
					if v, ok := EvalJSON(workDir, statePath, varFiles, bb.String(), 100*time.Millisecond); ok {
						if m, ok := v.(map[string]any); ok {
							for k, val := range m {
								resolved[k] = val
							}
						}
					}
				}
				if len(resolved) == 0 {
					continue
				}
				instRaw, _ := ref.obj["instances"].([]any)
				if instRaw == nil {
					instRaw = []any{}
				}
				if len(instRaw) == 0 {
					instAttrs := map[string]any{}
					for k, val := range resolved {
						instAttrs[k] = sanitizeValue(val)
					}
					inst := map[string]any{"attributes": instAttrs, "schema_version": 0}
					ref.obj["instances"] = []any{inst}
					resources[ref.idx] = ref.obj
					changed = true
				} else {
					for j := range instRaw {
						im, ok := instRaw[j].(map[string]any)
						if !ok {
							continue
						}
						attrs, _ := im["attributes"].(map[string]any)
						if attrs == nil {
							attrs = map[string]any{}
							im["attributes"] = attrs
						}
						for k, val := range resolved {
							nv := sanitizeValue(val)
							ov, exists := attrs[k]
							if !exists || !deepEqualJSONish(ov, nv) {
								attrs[k] = nv
								changed = true
							}
						}
					}
					ref.obj["instances"] = instRaw
					resources[ref.idx] = ref.obj
				}
			}
		}
	}
	if !changed {
		return nil
	}
	st["resources"] = resources
	// bump minimal serial
	switch v := st["version"].(type) {
	case float64:
		if v == 0 {
			st["version"] = 4
		}
	case int:
		if v == 0 {
			st["version"] = 4
		}
	default:
		st["version"] = 4
	}
	switch s := st["serial"].(type) {
	case float64:
		if s <= 0 {
			st["serial"] = 1
		} else {
			st["serial"] = int(s) + 1
		}
	case int:
		if s <= 0 {
			st["serial"] = 1
		} else {
			st["serial"] = s + 1
		}
	default:
		st["serial"] = 1
	}
	return writeStateAtomicRaw(statePath, st)
}

// PatchTargetedExactByFiles enumerates changed resources in the provided files and applies
// the exact single-attr patch approach to every non-meta attribute in those resources.
func PatchTargetedExactByFiles(rootDir, workDir, statePath string, varFiles []string, files []string) error {
	if len(files) == 0 {
		return nil
	}
	// Build evaluation context once per batch for fast in-process evaluation
	vars, locals := loadVarsAndLocals(workDir, varFiles)
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"var": ctyObjectFromMap(vars), "local": ctyObjectFromMap(locals)}, Functions: terraformFunctions()}
	varsStamp := computeVarsStamp(varFiles)

	// Bounded parallelism over files
	type job struct{ path string }
	jobs := make(chan job, len(files))
	const maxWorkers = 3
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for jb := range jobs {
			p := jb.path
			src, f, ok := getSyntaxFileCached(p)
			if !ok || f == nil || len(src) == 0 {
				continue
			}
			body, ok := f.Body.(*hclsyntax.Body)
			if !ok {
				continue
			}
			for _, blk := range body.Blocks {
				if blk == nil || blk.Type != "resource" || len(blk.Labels) < 2 {
					continue
				}
				rType, rName := blk.Labels[0], blk.Labels[1]
				// For each non-meta attribute in the changed block, patch exactly that attribute
				for attrName, a := range blk.Body.Attributes {
					if isMetaArg(attrName) {
						continue
					}
					isLit := false
					var litVal any
					var expr string
					if v, ok := constValue(a.Expr); ok {
						isLit = true
						litVal = v
					} else {
						r := a.Expr.Range()
						if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
							r = call.Args[0].Range()
						}
						if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
							expr = string(src[r.Start.Byte:r.End.Byte])
						}
					}
					_ = patchAttrValueExactWithCtx(ctx, varsStamp, workDir, statePath, varFiles, rType, rName, attrName, isLit, litVal, expr)
				}
			}
		}
	}
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go worker()
	}
	for _, p := range files {
		jobs <- job{path: p}
	}
	close(jobs)
	wg.Wait()
	return nil
}

func computeVarsStamp(varFiles []string) string {
	if len(varFiles) == 0 {
		return ""
	}
	b := strings.Builder{}
	for _, vf := range varFiles {
		fi, err := os.Stat(vf)
		if err != nil {
			continue
		}
		b.WriteString(vf)
		b.WriteByte('|')
		b.WriteString(fmt.Sprint(fi.ModTime().UnixNano()))
		b.WriteByte('|')
		b.WriteString(fmt.Sprint(fi.Size()))
		b.WriteByte(';')
	}
	return b.String()
}

// patchAttrValueExactWithCtx is like patchAttrValueExact but uses a prebuilt HCL eval context.
var evalMemoMu sync.Mutex
var evalMemo = map[string]any{}

func patchAttrValueExactWithCtx(ctx *hcl.EvalContext, varsStamp, workDir, statePath string, varFiles []string, rType, rName, attr string, isLiteral bool, lit any, expr string) error {
	var val any
	if isLiteral {
		val = lit
	} else if strings.TrimSpace(expr) != "" {
		key := workDir + "|" + varsStamp + "|" + rType + "|" + rName + "|" + attr + "|" + expr
		evalMemoMu.Lock()
		if cached, okm := evalMemo[key]; okm {
			val = cached
			evalMemoMu.Unlock()
		} else {
			evalMemoMu.Unlock()
		}
		if val == nil {
			if v, ok := evalExprWithCtx(ctx, expr); ok {
				val = v
			} else if v, ok := EvalJSON(workDir, statePath, varFiles, expr, 100*time.Millisecond); ok {
				val = v
			}
			if val != nil {
				evalMemoMu.Lock()
				evalMemo[key] = val
				evalMemoMu.Unlock()
			}
		}
	}
	if val == nil {
		return nil
	}
	return patchAttrWrite(statePath, rType, rName, attr, val)
}

func evalExprWithCtx(ctx *hcl.EvalContext, expr string) (any, bool) {
	tfExpr, diags := hclsyntax.ParseExpression([]byte(expr), "__attr__.tf", hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() || tfExpr == nil {
		return nil, false
	}
	v, diags := tfExpr.Value(ctx)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return nil, false
	}
	goV, ok := convertCtyToGo(v)
	if !ok {
		return nil, false
	}
	return goV, true
}

func patchAttrWrite(statePath, rType, rName, attr string, val any) error {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}
	// find matching type+name
	for i := range resources {
		m, ok := resources[i].(map[string]any)
		if !ok {
			continue
		}
		if mode, _ := m["mode"].(string); mode != "managed" {
			continue
		}
		if t, _ := m["type"].(string); t != rType {
			continue
		}
		if n, _ := m["name"].(string); n != rName {
			continue
		}
		if _, hasProv := m["provider"]; !hasProv {
			m["provider"] = providerAddressForType(rType)
		}
		instRaw, _ := m["instances"].([]any)
		if len(instRaw) == 0 {
			m["instances"] = []any{map[string]any{"attributes": map[string]any{attr: sanitizeValue(val)}, "schema_version": 0}}
			resources[i] = m
			st["resources"] = resources
			return writeStateBump(statePath, st, b)
		}
		changed := false
		for j := range instRaw {
			im, _ := instRaw[j].(map[string]any)
			if im == nil {
				continue
			}
			attrs, _ := im["attributes"].(map[string]any)
			if attrs == nil {
				attrs = map[string]any{}
				im["attributes"] = attrs
			}
			nv := sanitizeValue(val)
			ov, exists := attrs[attr]
			if !exists || !deepEqualJSONish(ov, nv) {
				attrs[attr] = nv
				changed = true
			}
		}
		if changed {
			m["instances"] = instRaw
			resources[i] = m
			st["resources"] = resources
			return writeStateBump(statePath, st, b)
		}
		return nil
	}
	// create new
	newRes := map[string]any{
		"mode":      "managed",
		"type":      rType,
		"name":      rName,
		"provider":  providerAddressForType(rType),
		"instances": []any{map[string]any{"attributes": map[string]any{attr: sanitizeValue(val)}, "schema_version": 0}},
	}
	st["resources"] = append(resources, newRes)
	return writeStateBump(statePath, st, b)
}

// PatchSpecificResourceAttrExact evaluates and patches a single attribute for one resource (type+name).
func PatchSpecificResourceAttrExact(rootDir, workDir, statePath string, varFiles []string, rType, rName, attr string) error {
	// Find the expression for this exact resource attr
	abs, _ := filepath.Abs(rootDir)
	var found attrExpr
	_ = filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
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
				brType, brName := blk.Labels[0], blk.Labels[1]
				if brType != rType || brName != rName {
					continue
				}
				if a, ok := blk.Body.Attributes[attr]; ok && a != nil {
					ae := attrExpr{ModulePath: nil, Type: rType, Name: rName, Attr: attr}
					if v, okc := constValue(a.Expr); okc {
						ae.IsLiteral = true
						ae.LitValue = v
					} else {
						r := a.Expr.Range()
						if call, ok := a.Expr.(*hclsyntax.FunctionCallExpr); ok && strings.EqualFold(call.Name, "jsonencode") && len(call.Args) == 1 {
							r = call.Args[0].Range()
						}
						if int(r.Start.Byte) >= 0 && int(r.End.Byte) <= len(src) && r.End.Byte >= r.Start.Byte {
							ae.Expr = string(src[r.Start.Byte:r.End.Byte])
						}
					}
					found = ae
					return io.EOF // stop walk
				}
			}
		}
		return nil
	})
	if found.Type == "" {
		return nil
	}
	// Evaluate value
	var val any
	if found.IsLiteral {
		val = found.LitValue
	} else if strings.TrimSpace(found.Expr) != "" {
		if v, ok := TryEvalInProcess(workDir, varFiles, found.Expr, 1*time.Second); ok {
			val = v
		} else if v, ok := EvalJSON(workDir, statePath, varFiles, found.Expr, 3*time.Second); ok {
			val = v
		}
	}
	if val == nil {
		return nil
	}
	// Patch state for only this resource name
	b, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}
	// locate correct object by type+name (module root)
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			if mode, _ := m["mode"].(string); mode != "managed" {
				continue
			}
			if t, _ := m["type"].(string); t != rType {
				continue
			}
			if n, _ := m["name"].(string); n != rName {
				continue
			}
			if _, hasProv := m["provider"]; !hasProv {
				m["provider"] = providerAddressForType(rType)
			}
			instRaw, _ := m["instances"].([]any)
			if len(instRaw) == 0 {
				m["instances"] = []any{map[string]any{"attributes": map[string]any{attr: sanitizeValue(val)}, "schema_version": 0}}
				resources[i] = m
				st["resources"] = resources
				return writeStateBump(statePath, st, b)
			}
			changed := false
			for j := range instRaw {
				im, _ := instRaw[j].(map[string]any)
				if im == nil {
					continue
				}
				attrs, _ := im["attributes"].(map[string]any)
				if attrs == nil {
					attrs = map[string]any{}
					im["attributes"] = attrs
				}
				nv := sanitizeValue(val)
				ov, exists := attrs[attr]
				if !exists || !deepEqualJSONish(ov, nv) {
					attrs[attr] = nv
					changed = true
				}
			}
			if changed {
				m["instances"] = instRaw
				resources[i] = m
				st["resources"] = resources
				return writeStateBump(statePath, st, b)
			}
			return nil
		}
	}
	// Not present: create new
	newRes := map[string]any{
		"mode":      "managed",
		"type":      rType,
		"name":      rName,
		"provider":  providerAddressForType(rType),
		"instances": []any{map[string]any{"attributes": map[string]any{attr: sanitizeValue(val)}, "schema_version": 0}},
	}
	st["resources"] = append(resources, newRes)
	return writeStateBump(statePath, st, b)
}

func writeStateBump(path string, st map[string]any, old []byte) error {
	// bump serial/version minimal
	switch v := st["version"].(type) {
	case float64:
		if v == 0 {
			st["version"] = 4
		}
	case int:
		if v == 0 {
			st["version"] = 4
		}
	default:
		st["version"] = 4
	}
	switch s := st["serial"].(type) {
	case float64:
		if s <= 0 {
			st["serial"] = 1
		} else {
			st["serial"] = int(s) + 1
		}
	case int:
		if s <= 0 {
			st["serial"] = 1
		} else {
			st["serial"] = s + 1
		}
	default:
		st["serial"] = 1
	}
	nb, _ := json.Marshal(st)
	if len(old) == len(nb) && string(old) == string(nb) {
		return nil
	}
	return writeStateAtomicRaw(path, st)
}
