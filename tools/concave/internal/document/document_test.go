package document

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}
func TestMDXCommentsUseSyntaxAndRawUTF8Offsets(t *testing.T) {
	source := "---\ntitle: Café\n---\n\n{/* concave:begin audience\nwhy: Intentional audience.\nwitness: test.mjs\n*/}\n\nUse `bijection`.\n{/* concave:end audience */}\n\n```mdx\n{/* concave:begin fake */}\n```\n\nexport const example = `{/* concave:begin string */}`;\n"
	e := New(context.Background(), root(t))
	f, err := e.Parse("docs/page.mdx", source)
	if err != nil || f.Error != "" {
		t.Fatalf("%v %+v", err, f)
	}
	if len(f.Comments) != 2 {
		t.Fatalf("comments: %+v", f.Comments)
	}
	lines := f.CommentLines(source)
	if lines[4] != "concave:begin audience" || lines[7] != "" || lines[10] != "concave:end audience" {
		t.Fatal(lines)
	}
	for _, v := range lines {
		if strings.Contains(v, "fake") || strings.Contains(v, "string") {
			t.Fatal(lines)
		}
	}
	used := e.bytes
	if _, err = e.Parse("docs/page.mdx", source); err != nil || used != e.bytes {
		t.Fatal("cache did not reuse exact bytes")
	}
}
func TestMarkdownCommentsAndParserFailures(t *testing.T) {
	e := New(context.Background(), root(t))
	f, err := e.Parse("page.md", "<!-- concave:begin example\nwhy: text\nwitness: test.mjs\n-->\nbody\n<!-- concave:end example -->\n")
	if err != nil || f.Error != "" || len(f.Comments) != 2 {
		t.Fatalf("%v %+v", err, f)
	}
	f, err = e.Parse("page.mdx", "<Unclosed>\n")
	if err != nil || f.Error == "" {
		t.Fatalf("%v %+v", err, f)
	}
}
func TestExecutableFrontmatterIsNeverEvaluated(t *testing.T) {
	e := New(context.Background(), root(t))
	for _, language := range []string{"js", "javascript"} {
		facts, err := e.Parse("page.mdx", "---"+language+"\n(()=>{ throw new Error('evaluated'); })()\n---\n# Text\n")
		if err != nil || facts.Error != "Executable frontmatter is not supported" {
			t.Fatalf("%s: %v %+v", language, err, facts)
		}
	}
}

func TestRecoveryRejectsExecutableFrontmatterBeforeLoadingRecipe(t *testing.T) {
	e := New(context.Background(), root(t))
	unsafe := "---js\n(()=>{ throw new Error('evaluated'); })()\n---\n# Text\n"
	for _, pair := range [][2]string{{unsafe, "# Safe\n"}, {"# Safe\n", unsafe}} {
		_, err := e.Recover("page.mdx", "original.mdx", pair[0], pair[1], nil)
		if err == nil || !strings.Contains(err.Error(), "Executable frontmatter is not supported") {
			t.Fatal("recovery accepted executable frontmatter", err)
		}
	}
}

func TestRejectForgedRangesAndSources(t *testing.T) {
	source := Source{"page.mdx", "é text"}
	for _, facts := range []Facts{{Path: "other"}, {Path: source.Path, SHA256: "wrong"}, {Path: source.Path, SHA256: "", Comments: []Range{{-1, 1, "x"}}}} {
		if validate(source, facts) == nil {
			t.Fatal("forged facts accepted")
		}
	}
}

func TestFrontmatterCommentsAreLinesTheYAMLEngineIgnores(t *testing.T) {
	source := "---\n# concave:begin title\n# why: Product name.\ntitle: Café\nbody: |\n  # inside a block scalar\n  text\nquoted: \"a\n# inside quoted text\n  b\"\n  # indented comment\n# concave:end title\n---\n\n# Heading, not YAML\n"
	e := New(context.Background(), root(t))
	f, err := e.Parse("docs/page.mdx", source)
	if err != nil || f.Error != "" {
		t.Fatalf("%v %+v", err, f)
	}
	start, end, ok := f.FrontmatterLines(source)
	if !ok || start != 1 || end != 12 {
		t.Fatal(start, end, ok)
	}
	lines := f.CommentLines(source)
	want := map[int]string{1: "concave:begin title", 2: "why: Product name.", 10: "indented comment", 11: "concave:end title"}
	if len(lines) != len(want) {
		t.Fatal(lines)
	}
	for line, text := range want {
		if lines[line] != text {
			t.Fatal(lines)
		}
	}
	for _, page := range []string{"# No frontmatter\n", "---\n---\nbody\n"} {
		f, err = e.Parse("docs/other.mdx", page)
		if err != nil || f.Error != "" || len(f.Comments) != 0 {
			t.Fatalf("%v %+v", err, f)
		}
	}
	if _, _, ok = f.FrontmatterLines("---\n---\nbody\n"); !ok {
		t.Fatal("empty frontmatter has an empty line span")
	}
}

// Loading the parser dominates small pages, so one process parses a frontier.
func TestBatchParsesAFrontierInOneProcess(t *testing.T) {
	e := New(context.Background(), root(t))
	sources := []Source{}
	for i := 0; i < 130; i++ {
		sources = append(sources, Source{Path: fmt.Sprintf("docs/page-%03d.mdx", i), Source: fmt.Sprintf("# Page %d\n", i)})
	}
	if err := e.Batch(sources); err != nil || e.Calls != 1 {
		t.Fatal(err, e.Calls)
	}
	for _, s := range sources {
		if f, err := e.Parse(s.Path, s.Source); err != nil || f.Error != "" || e.Calls != 1 {
			t.Fatal(err, f, e.Calls)
		}
	}
}
