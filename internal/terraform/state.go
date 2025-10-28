package terraform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// tfState models the minimal fields we need to read/write a Terraform state file.
// This is intentionally minimal; unknown fields are ignored by json package.
// legacy struct types retained earlier are no longer used; operate on raw JSON

// EnsureStateInitialized creates a minimal local state file if it does not exist.
// The directory is created with 0700 and the state file with 0600 permissions.
func EnsureStateInitialized(statePath string) error {
	if strings.TrimSpace(statePath) == "" {
		return errors.New("state path is empty")
	}
	if fi, err := os.Stat(statePath); err == nil && !fi.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	st := map[string]any{
		"version":   4,
		"serial":    1,
		"lineage":   uuid.NewString(),
		"outputs":   map[string]any{},
		"resources": []any{},
	}
	return writeStateAtomicRaw(statePath, st)
}

// PatchStateFromConfig scans Terraform configuration under rootDir and merges
// discovered managed resources into the local state at statePath. Existing
// resources have their attributes updated for keys present in configuration;
// new resources are added with minimal instances containing literal attributes.
func PatchStateFromConfig(rootDir, statePath string, varFiles []string) error {
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(statePath) == "" {
		return errors.New("rootDir/statePath required")
	}
	if err := EnsureStateInitialized(statePath); err != nil {
		return err
	}
	// Load raw state preserving unknown fields
	st, b, _, err := readStateCached(statePath)
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if st["outputs"] == nil {
		st["outputs"] = map[string]any{}
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}

	cfgs, err := BuildResourceConfigsEvaluated(rootDir, rootDir, statePath, varFiles)
	if err != nil {
		// Non-fatal: still write back state to ensure file presence
		return fmt.Errorf("scan config: %w", err)
	}
	// Build index for quick lookup by module|type|name
	type resRef struct {
		idx int
		obj map[string]any
	}
	index := map[string]resRef{}
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			rType, _ := m["type"].(string)
			rName, _ := m["name"].(string)
			mode, _ := m["mode"].(string)
			if mode != "managed" {
				continue
			}
			mod, _ := m["module"].(string)
			key := resourceKey(mod, rType, rName)
			index[key] = resRef{idx: i, obj: m}
		}
	}

	changed := false
	for _, rc := range cfgs {
		mod := modulePathToString(rc.ModulePath)
		key := resourceKey(mod, rc.Type, rc.Name)
		if ref, ok := index[key]; ok {
			// Ensure provider is set for existing resources
			if _, hasProv := ref.obj["provider"]; !hasProv {
				ref.obj["provider"] = providerAddressForType(rc.Type)
			}
			// Update all instances' attributes with keys from config
			instRaw, _ := ref.obj["instances"].([]any)
			if instRaw == nil {
				instRaw = []any{}
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
				// Only update attrs that actually changed
				for k, v := range rc.Attrs {
					nv := sanitizeValue(v)
					ov, exists := attrs[k]
					if !exists {
						attrs[k] = nv
						changed = true
						continue
					}
					if !deepEqualJSONish(ov, nv) {
						attrs[k] = nv
						changed = true
					}
				}
			}
			if len(instRaw) == 0 {
				inst := map[string]any{
					"attributes":     sanitizeMap(rc.Attrs),
					"schema_version": 0,
				}
				ref.obj["instances"] = []any{inst}
				changed = true
			} else {
				ref.obj["instances"] = instRaw
			}
			resources[ref.idx] = ref.obj
			continue
		}
		// Not found: add new minimal managed resource entry
		newRes := map[string]any{
			"mode":     "managed",
			"type":     rc.Type,
			"name":     rc.Name,
			"provider": providerAddressForType(rc.Type),
			"instances": []any{map[string]any{
				"attributes":     sanitizeMap(rc.Attrs),
				"schema_version": 0,
			}},
		}
		if mod != "" {
			newRes["module"] = mod
		}
		resources = append(resources, newRes)
		index[key] = resRef{idx: len(resources) - 1, obj: newRes}
		changed = true
	}

	st["resources"] = resources

	// If nothing changed, avoid bumping serial or rewriting the file
	if !changed {
		return nil
	}

	// Increment serial and ensure version
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

	// Serialize once and skip write if identical to original bytes
	newBytes, mErr := json.Marshal(st)
	if mErr != nil {
		return mErr
	}
	if len(b) == len(newBytes) && string(b) == string(newBytes) {
		return nil
	}
	return writeStateAtomicRaw(statePath, st)
}

// PatchStateFromConfigLiterals is a faster variant that scans configuration and
// merges only literal attributes (no terraform console evaluation). This avoids
// subprocess overhead on every refresh and is suitable for near-instant updates.
func PatchStateFromConfigLiterals(rootDir, statePath string) error {
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(statePath) == "" {
		return errors.New("rootDir/statePath required")
	}
	if err := EnsureStateInitialized(statePath); err != nil {
		return err
	}
	// Load raw state
	st, _, _, err := readStateCached(statePath)
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	if st["outputs"] == nil {
		st["outputs"] = map[string]any{}
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}

	cfgs, err := BuildResourceConfigs(rootDir)
	if err != nil {
		return fmt.Errorf("scan config: %w", err)
	}

	// Build index for quick lookup by module|type|name
	type resRef struct {
		idx int
		obj map[string]any
	}
	index := map[string]resRef{}
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			rType, _ := m["type"].(string)
			rName, _ := m["name"].(string)
			mode, _ := m["mode"].(string)
			if mode != "managed" {
				continue
			}
			mod, _ := m["module"].(string)
			key := resourceKey(mod, rType, rName)
			index[key] = resRef{idx: i, obj: m}
		}
	}

	changed := false
	for _, rc := range cfgs {
		mod := modulePathToString(rc.ModulePath)
		key := resourceKey(mod, rc.Type, rc.Name)
		if ref, ok := index[key]; ok {
			// Ensure provider is set for existing resources
			if _, hasProv := ref.obj["provider"]; !hasProv {
				ref.obj["provider"] = providerAddressForType(rc.Type)
			}
			instRaw, _ := ref.obj["instances"].([]any)
			if instRaw == nil {
				instRaw = []any{}
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
				for k, v := range rc.Attrs {
					nv := sanitizeValue(v)
					ov, exists := attrs[k]
					if !exists || !deepEqualJSONish(ov, nv) {
						attrs[k] = nv
						changed = true
					}
				}
			}
			if len(instRaw) == 0 {
				inst := map[string]any{
					"attributes":     sanitizeMap(rc.Attrs),
					"schema_version": 0,
				}
				ref.obj["instances"] = []any{inst}
				changed = true
			} else {
				ref.obj["instances"] = instRaw
			}
			resources[ref.idx] = ref.obj
			continue
		}
		// Not found: add new minimal managed resource entry
		newRes := map[string]any{
			"mode":     "managed",
			"type":     rc.Type,
			"name":     rc.Name,
			"provider": providerAddressForType(rc.Type),
			"instances": []any{map[string]any{
				"attributes":     sanitizeMap(rc.Attrs),
				"schema_version": 0,
			}},
		}
		if mod != "" {
			newRes["module"] = mod
		}
		resources = append(resources, newRes)
		index[key] = resRef{idx: len(resources) - 1, obj: newRes}
		changed = true
	}

	st["resources"] = resources
	if !changed {
		return nil
	}

	// Increment serial/version and write compactly
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

// PatchStateFromConfigEvaluatedFast performs a single global-batch evaluation of non-literal
// attributes using terraform console and merges results into the state. Intended to run
// after the literal fast-path to quickly incorporate vars/locals/for-expressions.
func PatchStateFromConfigEvaluatedFast(rootDir, workDir, statePath string, varFiles []string) error {
	if strings.TrimSpace(rootDir) == "" || strings.TrimSpace(statePath) == "" {
		return errors.New("rootDir/statePath required")
	}
	if err := EnsureStateInitialized(statePath); err != nil {
		return err
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	if st["outputs"] == nil {
		st["outputs"] = map[string]any{}
	}
	resources, _ := st["resources"].([]any)
	if resources == nil {
		resources = []any{}
	}

	// Try fast global-batch evaluation first (single eval via persistent console)
	cfgs, gerr := BuildResourceConfigsEvaluatedGlobal(rootDir, workDir, statePath, varFiles)
	if gerr != nil || len(cfgs) == 0 {
		// Fall back to robust per-resource evaluation with per-attribute fallback
		var perr error
		cfgs, perr = BuildResourceConfigsEvaluated(rootDir, workDir, statePath, varFiles)
		if perr != nil {
			return fmt.Errorf("scan config: %w", perr)
		}
	}

	type resRef struct {
		idx int
		obj map[string]any
	}
	index := map[string]resRef{}
	for i := range resources {
		if m, ok := resources[i].(map[string]any); ok {
			rType, _ := m["type"].(string)
			rName, _ := m["name"].(string)
			mode, _ := m["mode"].(string)
			if mode != "managed" {
				continue
			}
			mod, _ := m["module"].(string)
			key := resourceKey(mod, rType, rName)
			index[key] = resRef{idx: i, obj: m}
		}
	}

	changed := false
	for _, rc := range cfgs {
		mod := modulePathToString(rc.ModulePath)
		key := resourceKey(mod, rc.Type, rc.Name)
		if ref, ok := index[key]; ok {
			// Ensure provider is set for existing resources
			if _, hasProv := ref.obj["provider"]; !hasProv {
				ref.obj["provider"] = providerAddressForType(rc.Type)
			}
			instRaw, _ := ref.obj["instances"].([]any)
			if instRaw == nil {
				instRaw = []any{}
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
				for k, v := range rc.Attrs {
					nv := sanitizeValue(v)
					ov, exists := attrs[k]
					if !exists || !deepEqualJSONish(ov, nv) {
						attrs[k] = nv
						changed = true
					}
				}
			}
			if len(instRaw) == 0 {
				inst := map[string]any{
					"attributes":     sanitizeMap(rc.Attrs),
					"schema_version": 0,
				}
				ref.obj["instances"] = []any{inst}
				changed = true
			} else {
				ref.obj["instances"] = instRaw
			}
			resources[ref.idx] = ref.obj
			continue
		}
		newRes := map[string]any{
			"mode":     "managed",
			"type":     rc.Type,
			"name":     rc.Name,
			"provider": providerAddressForType(rc.Type),
			"instances": []any{map[string]any{
				"attributes":     sanitizeMap(rc.Attrs),
				"schema_version": 0,
			}},
		}
		if mod != "" {
			newRes["module"] = mod
		}
		resources = append(resources, newRes)
		index[key] = resRef{idx: len(resources) - 1, obj: newRes}
		changed = true
	}

	st["resources"] = resources
	if !changed {
		return nil
	}
	// Increment serial/version and write compactly
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

func writeStateAtomicRaw(path string, st map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Use compact JSON to minimize bytes written and speed up comparisons
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// State changed; update evaluator snapshots to avoid restarts and lock contention
	UpdatePersistentEvaluatorSnapshots(path, b)
	return nil
}

// readStateCached is a lightweight helper that reads and parses the state file.
// For now it does no real caching; it returns cacheHit=false.
func readStateCached(path string) (map[string]any, []byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, err
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, nil, false, err
	}
	return st, b, false, nil
}

func resourceKey(module, rType, name string) string {
	if module == "" {
		return rType + "|" + name
	}
	return module + "|" + rType + "|" + name
}

func modulePathToString(path []string) string {
	if len(path) == 0 {
		return ""
	}
	// Terraform state uses module path like: module.a.module.b
	s := ""
	for i, p := range path {
		if i > 0 {
			s += "."
		}
		s += "module." + p
	}
	return s
}

// cloneMap was used in earlier versions; replaced by sanitizeMap

func sanitizeMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = sanitizeValue(v)
	}
	return out
}

// sanitizeValue converts stringified JSON and over-quoted strings into proper types.
// It also recursively sanitizes arrays and objects.
func sanitizeValue(v any) any {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		// Try to unquote once (e.g., "foo" -> foo)
		if uq, err := strconv.Unquote(s); err == nil {
			s = uq
		}
		// If looks like JSON object/array/primitive, try to parse
		if len(s) > 0 && (s[0] == '{' || s[0] == '[' || s[0] == '"' || s[0] == 't' || s[0] == 'f' || s[0] == 'n' || s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
			var parsed any
			if json.Unmarshal([]byte(s), &parsed) == nil {
				return sanitizeValue(parsed)
			}
		}
		return s
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = sanitizeValue(t[i])
		}
		return out
	case map[string]any:
		return sanitizeMap(t)
	default:
		return v
	}
}

// deepEqualJSONish compares two JSON-like values accounting for equivalent numeric representations.
// It is tailored for state attribute maps produced by sanitizeValue.
func deepEqualJSONish(a, b any) bool {
	// Fast path
	if a == nil || b == nil {
		return a == b
	}
	switch av := a.(type) {
	case float64:
		switch bv := b.(type) {
		case float64:
			return av == bv
		case int:
			return av == float64(bv)
		}
	case int:
		switch bv := b.(type) {
		case float64:
			return float64(av) == bv
		case int:
			return av == bv
		}
	case string:
		if bs, ok := b.(string); ok {
			return av == bs
		}
	case bool:
		if bb, ok := b.(bool); ok {
			return av == bb
		}
	case []any:
		bb, ok := b.([]any)
		if !ok || len(av) != len(bb) {
			return false
		}
		for i := range av {
			if !deepEqualJSONish(av[i], bb[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bb, ok := b.(map[string]any)
		if !ok || len(av) != len(bb) {
			return false
		}
		for k, v := range av {
			if !deepEqualJSONish(v, bb[k]) {
				return false
			}
		}
		return true
	}
	// Fallback to string compare for other comparable types
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// stringsTrim no longer needed; using strings.TrimSpace directly

// providerAddressForType derives a Terraform provider address for a given resource type.
// Example: "azurerm_kubernetes_cluster" -> "provider[\"registry.terraform.io/hashicorp/azurerm\"]"
func providerAddressForType(resourceType string) string {
	prov := resourceType
	if i := strings.Index(resourceType, "_"); i > 0 {
		prov = resourceType[:i]
	}
	host := "registry.terraform.io"
	namespace := "hashicorp"
	// Common providers are under hashicorp namespace; unknowns still default there
	switch prov {
	case "azurerm", "aws", "google", "random", "null", "tls", "time", "archive", "template", "kubernetes":
		// keep defaults
	default:
		// keep defaults; callers may adjust later if needed
	}
	return fmt.Sprintf("provider[\"%s/%s/%s\"]", host, namespace, prov)
}
