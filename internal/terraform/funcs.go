package terraform

import (
	"fmt"
	"strings"

	cty "github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// terraformFunctions provides a minimal set of Terraform-like functions to resolve
// common non-literals in-process without spawning terraform console.
func terraformFunctions() map[string]function.Function {
	return map[string]function.Function{
		"lower": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "s", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return cty.StringVal(strings.ToLower(args[0].AsString())), nil
			},
		}),
		"upper": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "s", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return cty.StringVal(strings.ToUpper(args[0].AsString())), nil
			},
		}),
		"tostring": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "v", Type: cty.DynamicPseudoType}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return cty.StringVal(args[0].GoString()), nil
			},
		}),
		"join": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "sep", Type: cty.String}, {Name: "list", Type: cty.List(cty.String)}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				sep := args[0].AsString()
				it := args[1].ElementIterator()
				parts := []string{}
				for it.Next() {
					_, v := it.Element()
					parts = append(parts, v.AsString())
				}
				return cty.StringVal(strings.Join(parts, sep)), nil
			},
		}),
		"concat": function.New(&function.Spec{
			VarParam: &function.Parameter{Name: "lists", Type: cty.List(cty.DynamicPseudoType)},
			Type:     function.StaticReturnType(cty.List(cty.DynamicPseudoType)),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				// flatten list arguments
				out := []cty.Value{}
				for _, l := range args {
					it := l.ElementIterator()
					for it.Next() {
						_, v := it.Element()
						out = append(out, v)
					}
				}
				return cty.ListVal(out), nil
			},
		}),
		"format": function.New(&function.Spec{
			VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
			Params:   []function.Parameter{{Name: "fmt", Type: cty.String}},
			Type:     function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				if len(args) == 0 {
					return cty.UnknownVal(cty.String), nil
				}
				f := args[0].AsString()
				vals := make([]any, 0, len(args)-1)
				for _, a := range args[1:] {
					vals = append(vals, a.GoString())
				}
				return cty.StringVal(fmt.Sprintf(f, vals...)), nil
			},
		}),
		"coalesce": function.New(&function.Spec{
			VarParam: &function.Parameter{Name: "vals", Type: cty.DynamicPseudoType},
			Type:     function.StaticReturnType(cty.DynamicPseudoType),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				for _, a := range args {
					if !a.IsNull() && a.IsKnown() {
						// prefer non-empty strings
						if a.Type() == cty.String && a.AsString() == "" {
							continue
						}
						return a, nil
					}
				}
				return cty.NullVal(cty.DynamicPseudoType), nil
			},
		}),
		"replace": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "s", Type: cty.String}, {Name: "substr", Type: cty.String}, {Name: "repl", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return cty.StringVal(strings.ReplaceAll(args[0].AsString(), args[1].AsString(), args[2].AsString())), nil
			},
		}),
	}
}
