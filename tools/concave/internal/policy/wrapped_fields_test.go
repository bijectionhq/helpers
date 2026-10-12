package policy_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
)

func TestAnnotationValueMayStartOnFollowingComment(t *testing.T) {
	for _, path := range []string{"x.rs", "x.proto", "Cargo.toml"} {
		t.Run(path, func(t *testing.T) {
			text := "// concave:begin read\n" +
				"// why:\n// Preserve the\n// current read.\n//\n" +
				"// witness:\n// other.rs::\n// tests::read\n//\n" +
				"// witness:\n// tests::another_read\n//\n" +
				"// remove-when:\n// Upstream owns\n// this read.\n" +
				"read();\n// concave:end read\n"
			if path == "Cargo.toml" {
				text = strings.ReplaceAll(text, "//", "#")
			}
			annotations, bad := policy.Annotations(path, text)
			a := annotations["read"]
			if len(bad) != 0 || a.Code != "read();\n" ||
				a.Explanation.Why != "Preserve the current read." ||
				strings.Join(a.Explanation.Witnesses, ",") != "other.rs::tests::read,tests::another_read" ||
				a.Explanation.RemoveWhen != "Upstream owns this read." {
				t.Fatal(annotations, bad)
			}
		})
	}
}

func TestAnnotationEmptyFieldsStillRefusedAfterContinuationParsing(t *testing.T) {
	for name, fields := range map[string]string{
		"why before next field":    "// why:\n// witness: tests::read\n",
		"first repeated witness":   header + "// witness:\n// witness: tests::other\n",
		"last witness before code": header + "// witness:\n//\n",
		"empty optional field":     header + "// remove-when:\n",
		"blank continuation":       header + "// remove-when:\n//    \n",
	} {
		t.Run(name, func(t *testing.T) {
			_, bad := policy.Annotations("x.rs", "// concave:begin read\n"+fields+"read();\n// concave:end read\n")
			found := false
			for _, f := range bad {
				if strings.HasPrefix(f.Message, "empty annotation field ") && f.Line > 1 {
					found = true
				}
			}
			if !found {
				t.Fatal("empty field was not diagnosed at its declaration", bad)
			}
		})
	}
}
