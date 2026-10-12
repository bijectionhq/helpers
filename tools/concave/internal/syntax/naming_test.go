package syntax

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNamingRealSyntaxFieldsAndSerialization(t *testing.T) {
	engine := os.Getenv("CONCAVE_AST_GREP")
	if engine == "" {
		path, _ := filepath.Abs("../../../../target/concave/engine/" + Version + "/ast-grep")
		engine = path
	}
	if _, err := os.Stat(engine); err != nil {
		t.Fatal("pinned syntax engine required", err)
	}
	e := NewPinned(context.Background(), engine)
	cases := []struct{ path, source, name, parent, value, wire string }{
		{"locals.go", "package p\nfunc f() { bad_name, other_name := 1, 2; _ = bad_name; _ = other_name }\n", "bad_name", "", "", ""},
		{"variables.go", "package p\nvar bad_name = 1\n", "bad_name", "", "", ""},
		{"api.ts", "export type Reply = { execution_id: string; ready: boolean; };", "ready", "Reply", "boolean", ""},
		{"api.rs", "struct Reply { #[serde(rename = \"executionId\")] execution_id: String, ready: bool }", "ready", "Reply", "boolean", "executionId"},
		{"api.go", "package p\ntype Reply struct { ExecutionID string `json:\"executionId\"`; Ready bool }\n", "Ready", "Reply", "boolean", "executionId"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			names, err := e.Names(c.path, []byte(c.source))
			if err != nil {
				t.Fatal(err)
			}
			found, wire := false, c.wire == ""
			for _, n := range names {
				found = found || n.Name == c.name && n.Parent == c.parent && n.Value == c.value
				wire = wire || n.Name == c.wire && n.Surface == "wire"
			}
			if !found || !wire {
				t.Fatal(names)
			}
		})
	}
}
