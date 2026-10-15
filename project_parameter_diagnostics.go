package documentcore

import (
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"slices"
)

// Select a stable field from the validator's leaf failures. Missing and
// additional properties have object instance locations, so extend those with
// the actual property name. Do not include values or an unbounded error tree
// in the public diagnostic; the form's schema supplies constraint details.
func projectParameterDiagnostic(root *jsonschema.ValidationError) (string, string) {
	var pointer, message string
	found := false
	choose := func(tokens []string, keyword string) {
		candidate := projectJSONPointer(tokens)
		if found && candidate >= pointer {
			return
		}
		found = true
		pointer = candidate
		switch keyword {
		case "required", "dependentRequired", "dependency":
			message = "required parameter is missing"
		case "additionalProperties", "unevaluatedProperties":
			message = "parameter is not allowed"
		case "type":
			message = "parameter has an invalid type"
		default:
			message = "parameter does not satisfy the schema constraint"
		}
	}
	var visit func(*jsonschema.ValidationError)
	visit = func(failure *jsonschema.ValidationError) {
		if failure == nil {
			return
		}
		if len(failure.Causes) > 0 {
			for _, cause := range failure.Causes {
				visit(cause)
			}
			return
		}
		keyword := ""
		if failure.ErrorKind != nil {
			if path := failure.ErrorKind.KeywordPath(); len(path) > 0 {
				keyword = path[0]
			}
		}
		var properties []string
		switch detail := failure.ErrorKind.(type) {
		case *kind.Required:
			properties = detail.Missing
		case *kind.DependentRequired:
			properties = detail.Missing
		case *kind.Dependency:
			properties = detail.Missing
		case *kind.AdditionalProperties:
			properties = detail.Properties
		case *kind.PropertyNames:
			properties = []string{detail.Property}
		}
		if len(properties) == 0 {
			choose(failure.InstanceLocation, keyword)
			return
		}
		for _, property := range properties {
			choose(append(slices.Clone(failure.InstanceLocation), property), keyword)
		}
	}
	visit(root)
	if !found {
		return projectJSONPointer(root.InstanceLocation), "parameter does not satisfy its schema"
	}
	return pointer, message
}
