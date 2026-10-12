package document

import (
	"concave/internal/repository"
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestDependencyFrontiersSharePartialsAndCloseCycles(t *testing.T) {
	sources := map[string]string{
		"docs/a.mdx":       "import P from './partial.mdx';\n\n<P />\n",
		"docs/b.mdx":       "import P from './partial.mdx';\n\n<P />\n",
		"docs/partial.mdx": "import P from './deep.mdx';\n\n<P />\n",
		"docs/deep.mdx":    "import P from './partial.mdx';\n\n<P />\n",
	}
	reads := map[string]int{}
	e := New(context.Background(), root(t))
	roots := []Source{{"docs/a.mdx", sources["docs/a.mdx"]}, {"docs/b.mdx", sources["docs/b.mdx"]}}
	graph, err := e.Dependencies(roots, func(path string) ([]byte, error) {
		reads[path]++
		s, ok := sources[path]
		if !ok {
			return nil, fmt.Errorf("absent")
		}
		return []byte(s), nil
	})
	if err != nil || len(graph.Problems) != 0 {
		t.Fatal(graph.Problems, err)
	}
	want := []string{"docs/deep.mdx", "docs/partial.mdx"}
	if !reflect.DeepEqual(graph.Dependencies["docs/a.mdx"], want) || !reflect.DeepEqual(graph.Dependencies["docs/b.mdx"], want) {
		t.Fatal(graph.Dependencies)
	}
	// Each document's identity covers only its own closure.
	if !reflect.DeepEqual(graph.Inputs["docs/a.mdx"], []string{"docs/a.mdx", "docs/deep.mdx", "docs/partial.mdx"}) ||
		!reflect.DeepEqual(graph.Inputs["docs/deep.mdx"], want) {
		t.Fatal(graph.Inputs)
	}
	if reads["docs/partial.mdx"] != 1 || reads["docs/deep.mdx"] != 1 || len(e.cache) != 4 {
		t.Fatal(reads, len(e.cache))
	}
	bytes := e.bytes
	if err = e.Batch(roots); err != nil || bytes != e.bytes {
		t.Fatal("cached roots parsed twice", err)
	}
}
func TestDependencyResolutionRejectsAmbiguityAndStaleAbsences(t *testing.T) {
	for _, stale := range []bool{false, true} {
		e := New(context.Background(), root(t))
		graph, err := e.Dependencies([]Source{{"docs/page.mdx", "import P from './partial';\n"}}, func(path string) ([]byte, error) {
			if path == "docs/partial.mdx" || path == "docs/partial.md" {
				return []byte("body"), nil
			}
			if stale && path == "docs/partial.ts" {
				return nil, &repository.Stale{Message: "input changed"}
			}
			return nil, fmt.Errorf("absent")
		})
		if stale {
			if !repository.IsStale(err) {
				t.Fatal(err)
			}
		} else if err != nil || len(graph.Problems) != 1 || graph.Problems[0].Kind != "missing_document_import" || graph.Problems[0].Import != "docs/partial" {
			t.Fatal(graph.Problems, err)
		} else if inputs := graph.Inputs["docs/page.mdx"]; len(inputs) != 11 || inputs[0] != "docs/page.mdx" {
			// The page, the bare import and its nine absent or ambiguous alternatives.
			t.Fatal(inputs)
		}
	}
}

// Reach admits exactly the documents holding a quoted literal that resolves to
// a target or to a stem the suffix probes extend to it, however it is spelled.
func TestReachResolvesEveryQuotedSpecifier(t *testing.T) {
	var reach Reach
	for _, target := range []string{"apps/docs/_partial.mdx", "npm-packages/docs/src/x/index.ts", "crates/core/lib.rs"} {
		reach.Add(target)
	}
	for _, c := range []struct {
		owner, source string
		want          bool
	}{
		{"apps/docs/a.mdx", "import P from './_partial';\n", true},
		{"apps/docs/sub/c.mdx", "import P from \"../_partial.mdx\";\n", true},
		{"apps/docs/d.mdx", "import P from './_p\\u0061rtial.mdx';\n", true},
		{"apps/docs/d.mdx", "import P from './_p\\u{61}rtial\\x2emdx';\n", true},
		{"apps/docs/d.mdx", "import P from './_par\\\ntial';\n", true},
		{"apps/docs/e.mdx", "import X from '@site/src/x';\n", true},
		{"npm-packages/docs/src/x/deep/f.mdx", "import X from '../';\n", true},
		{"apps/docs/g.mdx", "import S from '!!raw-loader!../../crates/core/lib.rs';\n", true},
		{"apps/docs/b.mdx", "See _partial, lib.rs and index for details.\n", false},
		{"apps/docs/h.mdx", "import S from 'convex/server';\nconst s = \"\\n\";\nimport O from './other';\n", false},
		{"apps/docs/i.mdx", "Don't './_partial\n", false},
		{"apps/docs/j.mdx", "import P from './\\1_partial';\n", false},
	} {
		if got := reach.May(c.owner, []byte(c.source)); got != c.want {
			t.Errorf("%s %q: %v", c.owner, c.source, got)
		}
	}
}

func TestStringLiteralDecodesModuleCodeEscapes(t *testing.T) {
	for source, want := range map[string]string{
		`'a\'b'`:             "a'b",
		`"\x41\u0042\u{43}"`: "ABC",
		`"\uD83D\u{DE00}"`:   "\U0001F600",
		`"\0"`:               "\x00",
		`"\q\n"`:             "q\n",
	} {
		if got, ok := stringLiteral([]byte(source)); !ok || got != want {
			t.Errorf("%s: %q %v", source, got, ok)
		}
	}
	for _, source := range []string{`"\1"`, `"\08"`, `"\x4"`, `"\u{}"`, `"\u{110000}"`, "\"a\nb\"", `"open`} {
		if got, ok := stringLiteral([]byte(source)); ok {
			t.Errorf("%s decoded as %q", source, got)
		}
	}
}
