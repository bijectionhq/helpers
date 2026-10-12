package syntax

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"concave/internal/repository"
)

func realEngine(t *testing.T) *Engine {
	t.Helper()
	configured := os.Getenv("CONCAVE_AST_GREP")
	if configured == "" {
		configured = "../../../../target/concave/engine/" + Version + "/ast-grep"
	}
	path, err := filepath.Abs(configured)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{ctx: context.Background(), path: path}
}
func importRecipe() Recipe {
	fix := "$PUBLIC"
	return Recipe{Language: "TypeScript", Fix: &fix,
		Rule:      json.RawMessage(`{"all":[{"pattern":"$MODULE"},{"kind":"string"},{"regex":"^[\"']convex/server[\"']$"},{"inside":{"kind":"import_statement"}}]}`),
		Transform: json.RawMessage(`{"PUBLIC":{"replace":{"source":"$MODULE","replace":"convex/server","by":"@bijectionhq/bijection/server"}}}`)}
}
func TestStructuralBatchScopeOffsetsAndExactBytes(t *testing.T) {
	recipe := importRecipe()
	cases := []struct{ path, before, after string }{
		{"named.ts", `import { x as y } from "convex/server";`, `import { x as y } from "@bijectionhq/bijection/server";`},
		{"single.ts", `import x from 'convex/server';`, `import x from '@bijectionhq/bijection/server';`},
		{"unicode.mts", "// é😀\r\nconst é='😀'; import {\r\n x,\r\n} from \"convex/server\";\r\n", "// é😀\r\nconst é='😀'; import {\r\n x,\r\n} from \"@bijectionhq/bijection/server\";\r\n"},
		{"types.cts", `import type { T as U } from "convex/server";`, `import type { T as U } from "@bijectionhq/bijection/server";`},
		{"view.tsx", "import x from \"convex/server\";\nconst v=<p>convex/server</p>;", "import x from \"@bijectionhq/bijection/server\";\nconst v=<p>convex/server</p>;"},
		{"negative.ts", "// import x from \"convex/server\";\nconst wire=\"convex/server\";\nimport x from \"other/server\";", "// import x from \"convex/server\";\nconst wire=\"convex/server\";\nimport x from \"other/server\";"},
	}
	inputs := []RewriteInput{}
	for _, tc := range cases {
		r := recipe
		r.Language = Language(tc.path)
		inputs = append(inputs, RewriteInput{tc.path, []byte(tc.before), r})
	}
	// Ambient ignore files cannot remove an explicitly scoped input from scanning.
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	if err := os.WriteFile(filepath.Join(temp, ".ignore"), []byte("*.ts\n*.tsx\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := realEngine(t)
	results, err := e.RewriteBatch(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if e.Calls != 2 {
		t.Fatalf("wanted version + one scan, got %d", e.Calls)
	}
	for i, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := results[i]
			if got.Err != nil || string(got.Source) != tc.after {
				t.Fatal(string(got.Source), got.Err)
			}
			for _, m := range got.Matches {
				if m.File != tc.path {
					t.Fatal(m.File)
				}
			}
			b, _, err := e.Rewrite(tc.path, []byte(tc.before), inputs[i].Recipe)
			if err != nil || string(b) != tc.after {
				t.Fatal(string(b), err)
			}
		})
	}
	if e.Calls != 2 {
		t.Fatal("classification rescanned prepared recipes", e.Calls)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".ignore" {
		t.Fatal("private input files leaked", entries, err)
	}
	// A different recipe for the same bytes must not reuse the earlier result.
	changed := recipe
	newFix := `"different"`
	changed.Fix = &newFix
	b, _, err := e.Rewrite(cases[0].path, []byte(cases[0].before), changed)
	if err != nil || !strings.Contains(string(b), "different") || e.Calls != 3 {
		t.Fatal(string(b), err, e.Calls)
	}
}
func TestStructuralBatchChunksAndIsolatesRules(t *testing.T) {
	e := realEngine(t)
	inputs := []RewriteInput{}
	for i := 0; i < 65; i++ {
		r := importRecipe()
		fix := strconv.Quote(fmt.Sprintf("owner-%d", i))
		r.Fix = &fix
		inputs = append(inputs, RewriteInput{fmt.Sprintf("file-%d.ts", i), []byte(`import x from "convex/server";`), r})
	}
	results, err := e.RewriteBatch(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if e.Calls != 3 {
		t.Fatal("wanted version + two bounded scans", e.Calls)
	}
	for i, r := range results {
		if r.Err != nil || string(r.Source) != fmt.Sprintf("import x from %q;", fmt.Sprintf("owner-%d", i)) {
			t.Fatal(i, string(r.Source), r.Err)
		}
	}
}
func TestNativeRuleTestsVerifyMatchIntent(t *testing.T) {
	e := realEngine(t)
	positive := Fixture{`import x from "convex/server";`, `import x from "@bijectionhq/bijection/server";`}
	negative := Fixture{`const s="convex/server";`, `const s="convex/server";`}
	got, err := e.TestFixtures("public-import", importRecipe(), []Fixture{positive, negative})
	if err != nil || !got.Passed || got.Cases != 2 {
		t.Fatal(got, err)
	}
	wrong := negative
	wrong.After = "changed"
	got, err = e.TestFixtures("public-import", importRecipe(), []Fixture{wrong})
	if err != nil || got.Passed || got.Error == "" {
		t.Fatal("missing native match passed", got, err)
	}
	if _, err = e.TestFixtures("public-import", importRecipe(), nil); err == nil {
		t.Fatal("zero native cases passed")
	}
}
func TestStructuralRefusals(t *testing.T) {
	for _, kind := range []string{"whole-file-suppression", "next-line-suppression", "inline-suppression", "reserved-string", "parse-error", "empty-match", "no-change", "wrong-language", "overlapping", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			r := importRecipe()
			source := `import x from "convex/server";`
			switch kind {
			case "whole-file-suppression":
				source = "// ast-grep-ignore\n\n" + source
			case "next-line-suppression":
				source = "// ast-grep-ignore: transform\n" + source
			case "inline-suppression":
				source += " // ast-grep-ignore"
			case "reserved-string":
				source += " const s='ast-grep-ignore';"
			case "parse-error":
				source += " function {"
			case "empty-match":
				source = `const x="convex/server";`
			case "no-change":
				fix := "$MODULE"
				r.Fix = &fix
			case "wrong-language":
				r.Language = "Rust"
			case "overlapping":
				r.Rule = json.RawMessage(`{"any":[{"kind":"import_statement"},{"kind":"string"}]}`)
				fix := "changed"
				r.Fix = &fix
			case "oversized":
				source = strings.Repeat(" ", repository.MaxBytes+1)
			}
			if _, _, err := realEngine(t).Transform("x.ts", []byte(source), r); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
func TestScanMatchExitStatus(t *testing.T) {
	r := NativeRule("match", "TypeScript", map[string]any{"kind": "string"})
	r["severity"] = "error"
	matches, err := realEngine(t).Scan([]byte(`const x="convex";`), []map[string]any{r})
	if err != nil || len(matches) != 1 {
		t.Fatal(matches, err)
	}
}
func TestScanRejectsUntrustedRecords(t *testing.T) {
	source := []byte("é")
	base := Match{File: "STDIN", Language: "TypeScript", Rule: "match", Text: "é", Range: Range{Bytes: Offset{0, 2}, Start: Position{}, End: Position{0, 1}}}
	for _, kind := range []string{"unknown-file", "unknown-rule", "wrong-language", "mid-codepoint", "wrong-column", "wrong-text", "bad-replacement", "truncated", "invalid-json", "invalid-utf8", "wrong-capture"} {
		t.Run(kind, func(t *testing.T) {
			m := base
			switch kind {
			case "unknown-file":
				m.File = "elsewhere.ts"
			case "unknown-rule":
				m.Rule = "other"
			case "wrong-language":
				m.Language = "Rust"
			case "mid-codepoint":
				m.Range.Bytes.Start = 1
			case "wrong-column":
				m.Range.End.Column = 2
			case "wrong-text":
				m.Text = "x"
			case "bad-replacement":
				m.ReplacementOffsets = &Offset{0, 9}
				fix := "replacement"
				m.Replacement = &fix
			case "wrong-capture":
				m.Meta.Single = map[string]Capture{"NAME": {Text: "false", Range: m.Range}}
			}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "truncated":
				b = b[:len(b)-1]
			case "invalid-json":
				b = []byte("not-json")
			case "invalid-utf8":
				b = []byte{0xff}
			}
			path := filepath.Join(t.TempDir(), "engine")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + Identity + "'; else cat <<'RESPONSE'\n" + string(b) + "\nRESPONSE\nfi\n"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			e := &Engine{ctx: context.Background(), path: path}
			if _, err = e.Scan(source, []map[string]any{NativeRule("match", "TypeScript", map[string]any{"kind": "identifier"})}); !IsUnavailable(err) {
				t.Fatal("untrusted result accepted", err)
			}
		})
	}
}
func TestCancellationReapsOwnedEngine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine")
	pidPath := filepath.Join(dir, "pid")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + Identity + "'; else echo $$ > '" + pidPath + "'; exec sleep 30; fi\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{ctx: ctx, path: path}
	done := make(chan error, 1)
	go func() {
		_, err := e.RewriteBatch([]RewriteInput{{"x.ts", []byte(`import x from "convex/server";`), importRecipe()}})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	var pid int
	for pid == 0 {
		b, _ := os.ReadFile(pidPath)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		select {
		case <-deadline:
			t.Fatal("engine did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !IsUnavailable(err) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("engine was not reaped")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatal("owned engine survived cancellation", pid, err)
	}
}
func TestNativeSubsetRejectsIgnoredConfiguration(t *testing.T) {
	for _, raw := range []string{`{"kind":"string","ignores":["**"]}`, `{"kind":"string","severity":"off"}`, `{"matches":"external"}`, `{"inside":{"kind":"string","unknown":true}}`, `{"pattern":{"context":"$A","typo":"x"}}`, `{"any":[]}`} {
		r := importRecipe()
		r.Rule = json.RawMessage(raw)
		if err := r.Validate(); err == nil {
			t.Fatal("unsupported native field accepted", raw)
		}
	}
	r := importRecipe()
	r.Transform = json.RawMessage(`{"X":{"replace":{"source":"$MODULE","replace":"x","by":"y","extra":"ignored"}}}`)
	if err := r.Validate(); err == nil {
		t.Fatal("unknown transform field accepted")
	}
}
