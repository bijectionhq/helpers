package policy_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
)

func TestProtobufAnnotations(t *testing.T) {
	code := "  optional string incarnation = 10001;\n"
	for name, prefix := range map[string]string{
		"declarations":         "syntax = \"proto3\";\npackage fork;\nmessage Token {\n",
		"block comment":        "/*\n" + annotation("fake();\n") + "*/\n",
		"double quoted option": "option (note) = \"// concave:begin fake\";\n",
		"single quoted option": "option (note) = '// concave:begin fake';\n",
		"escaped quote":        "option (note) = \"quote \\\" // concave:begin fake\";\n",
		"trailing comment":     "package fork; // concave:begin fake\n",
	} {
		t.Run(name, func(t *testing.T) {
			as, bad := policy.Annotations("schema.proto", prefix+annotation(code))
			a := as["read"]
			if len(bad) != 0 || len(as) != 1 || a.Code != code {
				t.Fatal(as, bad)
			}
		})
	}
	_, bad := policy.Annotations("schema.proto", strings.Replace(annotation(code), "// concave:end read\n", "", 1))
	if len(bad) != 1 || bad[0].Kind != "invalid_fence" {
		t.Fatal("unclosed protobuf annotation accepted", bad)
	}
}
