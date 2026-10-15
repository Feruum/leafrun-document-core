package documentcore

import (
	"context"
	"testing"
)

func TestParameterValidationErrorsIdentifyTheAffectedField(t *testing.T) {
	for _, test := range []struct{ name, schema, raw, pointer string }{
		{"nested required", `{"type":"object","properties":{"customer":{"type":"object","required":["name"]}}}`, `{"customer":{}}`, "/customer/name"},
		{"nested type", `{"type":"object","properties":{"customer":{"type":"object","properties":{"name":{"type":"string"}}}}}`, `{"customer":{"name":1}}`, "/customer/name"},
		{"additional property", `{"type":"object","properties":{"customer":{"type":"object","additionalProperties":false}}}`, `{"customer":{"extra":"x"}}`, "/customer/extra"},
		{"array field", `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","required":["quantity"]}}}}`, `{"rows":[{}]}`, "/rows/0/quantity"},
		{"escaped required", `{"type":"object","properties":{"odd/name":{"type":"object","required":["~code"]}}}`, `{"odd/name":{}}`, "/odd~1name/~0code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareParameters(context.Background(), parameterSchemaFS(test.schema), "schemas/input.json", []byte(test.raw), nil)
			assertProjectDiagnostic(t, err, "invalid_parameters", test.pointer)
		})
	}
}
