// Package syntax is the bounded, pinned ast-grep adapter. It never edits source.
package syntax

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"concave/internal/repository"
)

const Version = "0.45.3"
const Identity = "ast-grep " + Version

type Unavailable struct{ Err error }

func (e *Unavailable) Error() string { return e.Err.Error() }
func (e *Unavailable) Unwrap() error { return e.Err }
func IsUnavailable(err error) bool   { var e *Unavailable; return errors.As(err, &e) }

type Offset struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}
type Range struct {
	Bytes Offset   `json:"byteOffset"`
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Capture struct {
	Text  string `json:"text"`
	Range Range  `json:"range"`
}
type Match struct {
	File               string  `json:"file"`
	Language           string  `json:"language"`
	Text               string  `json:"text"`
	Range              Range   `json:"range"`
	Rule               string  `json:"ruleId"`
	Replacement        *string `json:"replacement"`
	ReplacementOffsets *Offset `json:"replacementOffsets"`
	Meta               struct {
		Single map[string]Capture `json:"single"`
	} `json:"metaVariables"`
}

// Recipe embeds native ast-grep rules, but Concave owns identity, scope and edits.
// Fix is a string: expanding a fix outside its matched node is deliberately unsupported.
type Recipe struct {
	Language    string          `json:"language"`
	Rule        json.RawMessage `json:"rule"`
	Constraints json.RawMessage `json:"constraints,omitempty"`
	Transform   json.RawMessage `json:"transform,omitempty"`
	Fix         *string         `json:"fix"`
}
type Engine struct {
	ctx          context.Context
	path         string
	checked      bool
	Calls        int
	rewrites     map[string]RewriteResult
	comments     map[string]Comments
	rewriteBytes int
}

// NewPinned bypasses ambient tool selection for captured-source checks.
func NewPinned(ctx context.Context, path string) *Engine { return &Engine{ctx: ctx, path: path} }

func New(ctx context.Context, root string) *Engine {
	path := os.Getenv("CONCAVE_AST_GREP")
	if path == "" {
		path = filepath.Join(root, "target", "concave", "engine", Version, "ast-grep")
	}
	return &Engine{ctx: ctx, path: path}
}

// Executable pins the installed engine for checks in a temporary checkout.
// The candidate still verifies its version; exporting never installs a tool.
func (e *Engine) Executable() (string, error) {
	if err := e.Check(); err != nil {
		return "", err
	}
	path, err := exec.LookPath(e.path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
func Language(path string) string {
	switch filepath.Ext(path) {
	case ".rs":
		return "Rust"
	case ".ts", ".mts", ".cts":
		return "TypeScript"
	case ".tsx":
		return "Tsx"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".go":
		return "Go"
	}
	return ""
}

type bound struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *bound) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.exceeded = true
		return 0, fmt.Errorf("syntax output exceeds %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}
func (e *Engine) run(source []byte, args ...string) ([]byte, error) {
	return e.runAt(os.TempDir(), source, args...)
}

func (e *Engine) runAt(dir string, source []byte, args ...string) ([]byte, error) {
	if len(source) > repository.MaxBytes {
		return nil, fmt.Errorf("syntax input exceeds 4 MiB")
	}
	c := exec.CommandContext(e.ctx, e.path, args...)
	// Avoid discovery of a repository-controlled sgconfig or custom language library.
	c.Dir = dir
	c.Env = append(os.Environ(), "RAYON_NUM_THREADS=2")
	c.Stdin = bytes.NewReader(source)
	out, stderr := &bound{max: repository.MaxOutput}, &bound{max: 1 << 20}
	c.Stdout, c.Stderr = out, stderr
	e.Calls++
	err := c.Run()
	if out.exceeded || stderr.exceeded {
		return nil, &Unavailable{fmt.Errorf("syntax output exceeds response bound")}
	}
	if err != nil {
		failure := fmt.Errorf("ast-grep %s failed: %w: %s", Version, err, stderr.String())
		var exited *exec.ExitError
		if e.ctx.Err() != nil || !errors.As(err, &exited) || exited.ExitCode() < 0 {
			return nil, &Unavailable{failure}
		}
		if len(args) > 0 && args[0] == "scan" && exited.ExitCode() == 1 && out.Len() > 0 {
			// scan exits 1 for error-level matches. The caller still validates every record.
			return out.Bytes(), nil
		}
		return out.Bytes(), failure
	}
	return out.Bytes(), nil
}
func (e *Engine) Check() error {
	if e.checked {
		return nil
	}
	b, err := e.run(nil, "--version")
	if err != nil {
		return &Unavailable{fmt.Errorf("%w; install with concave engine install", err)}
	}
	if strings.TrimSpace(string(b)) != "ast-grep "+Version {
		return &Unavailable{fmt.Errorf("requires ast-grep %s, got %q", Version, strings.TrimSpace(string(b)))}
	}
	e.checked = true
	return nil
}
func sourceAllowed(source []byte) error {
	if len(source) > repository.MaxBytes || !utf8.Valid(source) {
		return fmt.Errorf("syntax input must be UTF-8 and at most 4 MiB")
	}
	// Reserve this token in all syntax inputs. A conservative refusal also covers
	// language-specific comment forms without relying on a suppressible scan.
	if bytes.Contains(source, []byte("ast-grep-ignore")) {
		return fmt.Errorf("syntax input contains reserved ast-grep-ignore token; Concave exceptions are the only suppression owner")
	}
	return nil
}

func encodeRules(rules []map[string]any) (string, error) {
	docs := make([]string, 0, len(rules))
	for _, r := range rules {
		b, err := json.Marshal(r)
		if err != nil {
			return "", err
		}
		docs = append(docs, string(b))
	}
	config := strings.Join(docs, "\n---\n")
	if len(config) > 64<<10 {
		return "", fmt.Errorf("syntax rules exceed 64 KiB")
	}
	return config, nil
}

type scanSource struct {
	source []byte
	rules  map[string]string
}

func validSpan(source []byte, span Offset) bool {
	return span.Start >= 0 && span.End >= span.Start && span.End <= len(source) &&
		(span.Start == len(source) || utf8.RuneStart(source[span.Start])) &&
		(span.End == len(source) || utf8.RuneStart(source[span.End]))
}
func decodeMatches(b []byte, sources map[string]scanSource) ([]Match, error) {
	if !utf8.Valid(b) {
		return nil, &Unavailable{fmt.Errorf("engine response is not UTF-8")}
	}
	matches := []Match{}
	d := json.NewDecoder(bytes.NewReader(b))
	// Index line starts once; columns from ast-grep count Unicode scalar values.
	lines := map[string][]int{}
	for path, s := range sources {
		starts := []int{0}
		for i, c := range s.source {
			if c == '\n' {
				starts = append(starts, i+1)
			}
		}
		lines[path] = starts
	}
	for {
		var m Match
		err := d.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &Unavailable{fmt.Errorf("invalid engine response: %w", err)}
		}
		s, ok := sources[m.File]
		language, known := s.rules[m.Rule]
		if !ok || !known || language != m.Language {
			return nil, &Unavailable{fmt.Errorf("engine returned unknown path, rule or language")}
		}
		validRange := func(r Range, text string) bool {
			if !validSpan(s.source, r.Bytes) || string(s.source[r.Bytes.Start:r.Bytes.End]) != text {
				return false
			}
			position := func(offset int) Position {
				starts := lines[m.File]
				line := sort.Search(len(starts), func(i int) bool { return starts[i] > offset }) - 1
				return Position{Line: line, Column: utf8.RuneCount(s.source[starts[line]:offset])}
			}
			return r.Start == position(r.Bytes.Start) && r.End == position(r.Bytes.End)
		}
		if !validRange(m.Range, m.Text) {
			return nil, &Unavailable{fmt.Errorf("engine returned invalid source range")}
		}
		for _, c := range m.Meta.Single {
			if !validRange(c.Range, c.Text) {
				return nil, &Unavailable{fmt.Errorf("engine returned invalid capture range")}
			}
		}
		if (m.Replacement == nil) != (m.ReplacementOffsets == nil) ||
			(m.ReplacementOffsets != nil && !validSpan(s.source, *m.ReplacementOffsets)) {
			return nil, &Unavailable{fmt.Errorf("engine returned invalid replacement range")}
		}
		matches = append(matches, m)
	}
	return matches, nil
}
func (e *Engine) Scan(source []byte, rules []map[string]any) ([]Match, error) {
	if err := sourceAllowed(source); err != nil {
		return nil, err
	}
	if err := e.Check(); err != nil {
		return nil, err
	}
	config, err := encodeRules(rules)
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, r := range rules {
		allowed[r["id"].(string)] = r["language"].(string)
	}
	b, err := e.run(source, "scan", "--stdin", "--json=stream", "--threads", "2", "--inline-rules", config, "--config", os.DevNull)
	if err != nil {
		return nil, err
	}
	return decodeMatches(b, map[string]scanSource{"STDIN": {source, allowed}})
}
func NativeRule(id, language string, rule any) map[string]any {
	return map[string]any{"id": id, "language": language, "rule": rule, "message": id, "severity": "hint"}
}
func recipeRule(id string, recipe Recipe) (map[string]any, error) {
	if err := recipe.Validate(); err != nil {
		return nil, err
	}
	rule := NativeRule(id, recipe.Language, recipe.Rule)
	rule["fix"] = *recipe.Fix
	if len(recipe.Constraints) > 0 {
		rule["constraints"] = recipe.Constraints
	}
	if len(recipe.Transform) > 0 {
		rule["transform"] = recipe.Transform
	}
	return rule, nil
}
func (e *Engine) Rewrite(path string, source []byte, recipe Recipe) ([]byte, []Match, error) {
	results, err := e.RewriteBatch([]RewriteInput{{Path: path, Source: source, Recipe: recipe}})
	if err != nil {
		return nil, nil, err
	}
	r := results[0]
	return r.Source, r.Matches, r.Err
}
func applyMatches(source []byte, matches []Match) ([]byte, []Match, error) {
	for _, m := range matches {
		if m.Rule == "parse-error" {
			return nil, nil, fmt.Errorf("cannot transform syntactically invalid source")
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Range.Bytes.Start < matches[j].Range.Bytes.Start })
	var out strings.Builder
	cursor := 0
	for _, m := range matches {
		span := m.Range.Bytes
		if span.Start < cursor || m.Replacement == nil || m.ReplacementOffsets == nil || *m.ReplacementOffsets != span {
			return nil, nil, fmt.Errorf("overlapping or expanded structural edits")
		}
		if out.Len()+span.Start-cursor+len(*m.Replacement) > repository.MaxBytes {
			return nil, nil, fmt.Errorf("transformed source exceeds 4 MiB")
		}
		out.Write(source[cursor:span.Start])
		out.WriteString(*m.Replacement)
		cursor = span.End
	}
	if out.Len()+len(source)-cursor > repository.MaxBytes {
		return nil, nil, fmt.Errorf("transformed source exceeds 4 MiB")
	}
	out.Write(source[cursor:])
	return []byte(out.String()), matches, nil
}

func (e *Engine) Transform(path string, source []byte, recipe Recipe) ([]byte, []Match, error) {
	want, matches, err := e.Rewrite(path, source, recipe)
	if err != nil {
		return nil, nil, err
	}
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("structural rule matches no upstream source")
	}
	if bytes.Equal(want, source) {
		return nil, nil, fmt.Errorf("structural rule makes no source change")
	}
	return want, matches, nil
}
