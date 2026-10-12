package policy

import (
	"concave/internal/document"
	"concave/internal/repository"
	"concave/internal/syntax"
	"fmt"
	"sort"
	"strings"
)

// Session owns syntax engines and captured-byte caches for one analysis. Config
// is declarative and may be shared; sessions never cross source operations.
type Session struct {
	Config    *Config
	Documents *document.Engine
	Syntax    *syntax.Engine
}

func NewSession(repo *repository.Repo, config *Config) *Session {
	return &Session{Config: config, Documents: document.New(repo.Context, repo.Root), Syntax: syntax.New(repo.Context, repo.Root)}
}

// Annotations reads fences from real comments only: the documentation parser
// for Markdown and MDX, the pinned syntax engine for TypeScript and JavaScript,
// and the lexical scanners for Rust, Go, protobuf and TOML.
func (s *Session) Annotations(path, text string) (map[string]Annotation, []Finding) {
	comments, err := s.commentLines(path, text)
	if err != nil {
		return nil, []Finding{{Kind: "invalid_fence", Path: path, Message: err.Error()}}
	}
	out, bad := annotations(path, text, comments)
	if document.Supports(path) && strings.Contains(text, "concave:") {
		facts, _ := s.documentFacts(path, text)
		if start, end, ok := facts.FrontmatterLines(text); ok {
			bad = append(bad, frontmatterFences(path, text, out, start, end)...)
		}
	}
	return out, bad
}

// commentLines maps standalone comments to zero-based lines. Text without the
// reserved prefix holds no annotation, so it is never parsed.
func (s *Session) commentLines(path, text string) (map[int]string, error) {
	switch {
	case !SupportsAnnotations(path) || !strings.Contains(text, "concave:"):
		return map[int]string{}, nil
	case document.Supports(path):
		facts, err := s.documentFacts(path, text)
		if err != nil {
			return nil, err
		}
		return facts.CommentLines(text), nil
	case syntax.CommentLanguage(path) != "":
		if s.Syntax == nil {
			return nil, fmt.Errorf("TypeScript and JavaScript annotations require the syntax engine")
		}
		facts, err := s.Syntax.CommentBatch([]syntax.CommentInput{{Path: path, Source: []byte(text)}})
		if err != nil {
			return nil, err
		}
		return ParsedComments(text, facts[0])
	}
	return StandaloneComments(path, text), nil
}

func (s *Session) documentFacts(path, text string) (document.Facts, error) {
	if s.Documents == nil {
		return document.Facts{}, fmt.Errorf("MDX annotation requires the repository parser")
	}
	facts, err := s.Documents.Parse(path, text)
	if err == nil && facts.Error != "" {
		err = fmt.Errorf("%s", facts.Error)
	}
	return facts, err
}

// PrepareComments parses every annotated TypeScript and JavaScript source of an
// analysis in bounded batches, so later annotation reads hit the session cache.
func (s *Session) PrepareComments(sources map[string]string) error {
	inputs := []syntax.CommentInput{}
	for path, text := range sources {
		if syntax.CommentLanguage(path) != "" && strings.Contains(text, "concave:") {
			inputs = append(inputs, syntax.CommentInput{Path: path, Source: []byte(text)})
		}
	}
	if len(inputs) == 0 {
		return nil
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	_, err := s.Syntax.CommentBatch(inputs)
	return err
}

// frontmatterFences keeps a frontmatter explanation per field: a fence that
// starts or ends among the YAML lines lies wholly inside them and encloses
// complete top-level fields, never part of a value or the page body.
func frontmatterFences(path, text string, fences map[string]Annotation, start, end int) []Finding {
	lines := repository.Lines(text)
	inside := func(line int) bool { return line >= start && line < end }
	field := func(line int) bool {
		return line < end && strings.TrimSpace(lines[line]) != "" && !strings.ContainsAny(lines[line][:1], " \t#-")
	}
	bad := []Finding{}
	for _, a := range fences {
		marker, code := a.CodeEnd, a.CodeStart
		if !inside(a.Start) && !inside(a.End-1) {
			continue
		}
		problem := ""
		switch {
		case !inside(a.Start) || !inside(a.End-1):
			problem = "a frontmatter fence must begin and end inside the frontmatter"
		case code < marker && !field(code):
			problem = "a frontmatter fence must enclose complete top-level fields; begin its code at a field name"
		case a.End < end && !field(a.End) && strings.TrimSpace(lines[a.End]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[a.End]), "#"):
			problem = "a frontmatter fence must enclose complete top-level fields; end it after a whole field"
		}
		if problem != "" {
			bad = append(bad, Finding{Kind: "invalid_fence", Path: path, Line: a.Start + 1, EndLine: a.End, Rule: "exception/valid", Exception: a.ID, Subject: Subject(strings.TrimSpace(lines[a.Start])), Message: problem})
		}
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].Line < bad[j].Line })
	return bad
}
