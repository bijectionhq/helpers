// Package document adapts MDX syntax into bounded, source-checked facts.
package document

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"concave/internal/artifact"
	"concave/internal/repository"
)

//go:embed adapter.mjs
var adapter []byte

func Identity() string { return "mdx " + repository.Digest(adapter) }
func Supports(path string) bool {
	return strings.HasSuffix(path, ".mdx") || strings.HasSuffix(path, ".md")
}

type Range struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}
type Import struct {
	Range
	Specifier string `json:"specifier"`
}

// Frontmatter, when present, spans the YAML lines between the delimiter lines.
type Facts struct {
	Path        string   `json:"path"`
	SHA256      string   `json:"sha256"`
	Comments    []Range  `json:"comments"`
	Imports     []Import `json:"imports"`
	Frontmatter *Range   `json:"frontmatter,omitempty"`
	Error       string   `json:"error"`
}
type Source struct {
	Path   string `json:"path"`
	Source string `json:"source"`
}
type Engine struct {
	// Calls counts parser processes started.
	Calls int
	node  string
	root  string
	ctx   context.Context
	cache map[string]Facts
	bytes int
}

func New(ctx context.Context, root string) *Engine {
	return &Engine{root: root, ctx: ctx, cache: map[string]Facts{}}
}
func key(path, source string) string { return path + "\x00" + repository.Digest([]byte(source)) }
func (e *Engine) Parse(path, source string) (Facts, error) {
	if err := e.Batch([]Source{{path, source}}); err != nil {
		return Facts{}, err
	}
	return e.cache[key(path, source)], nil
}
func (e *Engine) Batch(sources []Source) error {
	pending := []Source{}
	seen := map[string]bool{}
	for _, s := range sources {
		if !repository.ValidPath(s.Path) || !Supports(s.Path) || len(s.Source) > repository.MaxBytes || !repository.Text([]byte(s.Source)) {
			return fmt.Errorf("invalid MDX source %q", s.Path)
		}
		k := key(s.Path, s.Source)
		if _, ok := e.cache[k]; !ok && !seen[k] {
			pending = append(pending, s)
			seen[k] = true
		}
	}
	// One parser process per call: loading the parser dominates small pages,
	// so a frontier parses together. Count and bytes stay bounded.
	for len(pending) > 0 {
		batch := pending[:min(len(pending), maxBatch)]
		pending = pending[len(batch):]
		if err := e.run(batch); err != nil {
			return err
		}
	}
	return nil
}

const maxBatch = 4096

func (e *Engine) run(sources []Source) error {
	for _, s := range sources {
		e.bytes += len(s.Source)
	}
	if e.bytes > repository.MaxOutput {
		return fmt.Errorf("MDX input exceeds invocation bound")
	}
	request, _ := json.Marshal(struct {
		Format int      `json:"format"`
		Root   string   `json:"root"`
		Files  []Source `json:"files"`
	}{1, e.root, sources})
	result, err := e.execute(request, len(sources))
	if err != nil {
		return err
	}
	var reply struct {
		Format int     `json:"format"`
		Files  []Facts `json:"files"`
	}
	if err = artifact.Decode(result.Stdout, &reply); err != nil {
		return err
	}
	if reply.Format != 1 || len(reply.Files) != len(sources) {
		return fmt.Errorf("invalid MDX response")
	}
	for i, f := range reply.Files {
		s := sources[i]
		if err = validate(s, f); err != nil {
			return err
		}
		e.cache[key(s.Path, s.Source)] = f
	}
	return nil
}

// FrontmatterLines is the zero-based, end-exclusive line span of the YAML
// lines, or ok false when the document has none.
func (f Facts) FrontmatterLines(source string) (start, end int, ok bool) {
	if f.Frontmatter == nil {
		return 0, 0, false
	}
	return strings.Count(source[:f.Frontmatter.Start], "\n"), strings.Count(source[:f.Frontmatter.End], "\n"), true
}

func validate(s Source, f Facts) error {
	if f.Path != s.Path || f.SHA256 != repository.Digest([]byte(s.Source)) {
		return fmt.Errorf("MDX response source mismatch")
	}
	ranges := append([]Range{}, f.Comments...)
	for _, v := range f.Imports {
		ranges = append(ranges, v.Range)
	}
	if f.Frontmatter != nil {
		r := *f.Frontmatter
		if r.Start == 0 || r.Start > len(s.Source) || s.Source[r.Start-1] != '\n' || r.End > r.Start && s.Source[r.End-1] != '\n' {
			return fmt.Errorf("MDX response frontmatter mismatch")
		}
		ranges = append(ranges, r)
	}
	if len(ranges) > 4096 {
		return fmt.Errorf("MDX facts exceed bound")
	}
	for _, r := range ranges {
		if r.Start < 0 || r.End < r.Start || r.End > len(s.Source) || s.Source[r.Start:r.End] != r.Text {
			return fmt.Errorf("MDX response range mismatch")
		}
	}
	return nil
}

// Comments supplies the existing line-oriented annotation grammar, retaining raw
// document offsets. Delimiters are empty metadata lines, never approved body text.
// A frontmatter comment is one YAML line starting with '#'.
func (f Facts) CommentLines(source string) map[int]string {
	out := map[int]string{}
	for _, r := range f.Comments {
		start := strings.Count(source[:r.Start], "\n")
		text := r.Text
		if strings.HasPrefix(text, "#") {
			out[start] = strings.TrimSpace(strings.TrimPrefix(text, "#"))
			continue
		}
		if strings.HasPrefix(text, "{/*") {
			text = strings.TrimSuffix(strings.TrimPrefix(text, "{/*"), "*/}")
		} else {
			text = strings.TrimSuffix(strings.TrimPrefix(text, "<!--"), "-->")
		}
		for i, line := range strings.Split(text, "\n") {
			out[start+i] = strings.TrimSpace(line)
		}
	}
	return out
}
