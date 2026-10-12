package command_test

import (
	"concave/internal/analysis"
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeAnnotationErrorsCannotSilentlyPass(t *testing.T) {
	for _, text := range []string{
		"// concave:invriant identity\n// Keep the identity.\nfn owner() {}\n",
		"// concave:begin extension\n// why: Native behavior.\n// witness: crates/core/lib.rs\nfn owner() {}\n// concave:end extension\n",
		"// concave:end missing\nfn owner() {}\n",
	} {
		t.Run(strings.Split(text, "\n")[0], func(t *testing.T) {
			f := contractFixture(t)
			f.Write("crates/core/native.rs", text)
			for _, committed := range []bool{false, true} {
				if committed {
					f.Commit()
				}
				n, out, err := run(f, "check", "crates/core/native.rs", "--json")
				if n != 1 {
					t.Fatal("invalid native annotation accepted", n, out, err)
				}
				var r analysis.Report
				if err := json.Unmarshal([]byte(out), &r); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, v := range r.Findings {
					if v.Path == "crates/core/native.rs" && v.Line == 1 && v.Kind == "invalid_fence" {
						found = true
					}
				}
				if !found {
					t.Fatal("missing source-located annotation diagnostic", out)
				}
			}
		})
	}
}

func TestInvariantErrorsHaveStructuredSourceLocations(t *testing.T) {
	f := contractFixture(t)
	f.Write("crates/core/native.rs", "fn unrelated() {}\n\n// concave:invariant empty\nfn owner() {}\n")
	n, out, err := run(f, "check", "crates/core/native.rs", "--json")
	if n != 1 {
		t.Fatal(n, out, err)
	}
	var r analysis.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Line != 3 || r.Findings[0].Path != "crates/core/native.rs" {
		t.Fatal(r.Findings)
	}
}

func TestNativeAnnotationExamplesRemainSource(t *testing.T) {
	f := contractFixture(t)
	for path, text := range map[string]string{
		"crates/core/native.rs":         "const EXAMPLE: &str = r#\"\n// concave:begn fake\n\"#;\n/*\n// concave:end fake\n*/\n",
		"npm-packages/client/native.ts": "const example = `\n// concave:begn fake\n`;\n/*\n// concave:end fake\n*/\n",
	} {
		f.Write(path, text)
		if n, out, err := run(f, "check", path, "--json"); n != 0 {
			t.Fatal(n, out, err)
		}
	}
}
