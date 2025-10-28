package terraform

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	cty "github.com/zclconf/go-cty/cty"
)

// TryEvalInProcess attempts to evaluate an expression using HCL in-process with a
// best-effort subset of Terraform semantics: variables (var.*), locals (local.*),
// and standard cty functions from stdlib. Falls back to external console when false.
func TryEvalInProcess(workDir string, varFiles []string, expr string, timeout time.Duration) (any, bool) {
	if strings.TrimSpace(expr) == "" {
		return nil, false
	}
	// Build evaluation context from module variables (defaults + tfvars) and locals
	vars, locals := loadVarsAndLocals(workDir, varFiles)
	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":   ctyObjectFromMap(vars),
			"local": ctyObjectFromMap(locals),
		},
		Functions: terraformFunctions(),
	}
	// Parse expression as a snippet; file name is synthetic
	tfExpr, diags := hclsyntax.ParseExpression([]byte(expr), filepath.Join(workDir, "__expr__.tf"), hcl.Pos{Line: 1, Column: 1})
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

func loadVarsAndLocals(workDir string, varFiles []string) (map[string]cty.Value, map[string]cty.Value) {
	abs, _ := filepath.Abs(workDir)
	vars := map[string]cty.Value{}
	locals := map[string]cty.Value{}
	// Variable defaults via tfconfig
	if mod, diags := tfconfig.LoadModule(abs); diags == nil || !diags.HasErrors() {
		if mod != nil {
			for name, v := range mod.Variables {
				if v.Default != nil {
					if cv, ok := convertInterfaceToCty(v.Default); ok {
						vars[name] = cv
					}
				}
			}
		}
	}
	// Apply tfvars overrides
	for _, vf := range varFiles {
		if strings.TrimSpace(vf) == "" {
			continue
		}
		p := hclparse.NewParser()
		f, diags := p.ParseHCLFile(vf)
		if diags != nil && diags.HasErrors() || f == nil {
			continue
		}
		body := f.Body
		attrs, _ := body.JustAttributes()
		for k, a := range attrs {
			if v, d := a.Expr.Value(&hcl.EvalContext{}); d == nil || !d.HasErrors() {
				if v.IsWhollyKnown() {
					vars[k] = v
				}
			}
		}
	}
	// Compute locals by iterating until fixed point
	p := hclparse.NewParser()
	// Collect local attribute expressions across files
	type locAttr struct{ expr hcl.Expression }
	locExprs := map[string]locAttr{}
	_ = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".tf" {
			return nil
		}
		f, diags := p.ParseHCLFile(path)
		if diags != nil && diags.HasErrors() || f == nil {
			return nil
		}
		schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "locals"}}}
		content, _, _ := f.Body.PartialContent(schema)
		for _, b := range content.Blocks {
			attrs, _ := b.Body.JustAttributes()
			for name, a := range attrs {
				locExprs[name] = locAttr{expr: a.Expr}
			}
		}
		return nil
	})
	// Iteratively evaluate locals
	for i := 0; i < 4; i++ { // limit to prevent cycles
		progressed := false
		ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"var": ctyObjectFromMap(vars), "local": ctyObjectFromMap(locals)}, Functions: terraformFunctions()}
		for name, la := range locExprs {
			if _, exists := locals[name]; exists {
				continue
			}
			if v, diags := la.expr.Value(ctx); diags == nil || !diags.HasErrors() {
				if v.IsWhollyKnown() {
					locals[name] = v
					progressed = true
				}
			}
		}
		if !progressed {
			break
		}
	}
	return vars, locals
}

func ctyObjectFromMap(m map[string]cty.Value) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(m)
}

func convertInterfaceToCty(v interface{}) (cty.Value, bool) {
	switch t := v.(type) {
	case nil:
		return cty.NullVal(cty.DynamicPseudoType), true
	case string:
		return cty.StringVal(t), true
	case bool:
		return cty.BoolVal(t), true
	case float64:
		return cty.NumberFloatVal(t), true
	case int:
		return cty.NumberIntVal(int64(t)), true
	case []interface{}:
		arr := make([]cty.Value, 0, len(t))
		for _, e := range t {
			if cv, ok := convertInterfaceToCty(e); ok {
				arr = append(arr, cv)
			} else {
				return cty.NilVal, false
			}
		}
		return cty.TupleVal(arr), true
	case map[string]interface{}:
		m := map[string]cty.Value{}
		for k, e := range t {
			if cv, ok := convertInterfaceToCty(e); ok {
				m[k] = cv
			} else {
				return cty.NilVal, false
			}
		}
		return cty.ObjectVal(m), true
	default:
		return cty.NilVal, false
	}
}
