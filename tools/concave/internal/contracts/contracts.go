// Package contracts discovers behavioral obligations. It never authorizes edits
// or claims that prose has been proved by syntax validation.
package contracts

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"concave/internal/repository"
	"concave/internal/syntax"
)

var ID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Invariant struct {
	ID          string             `json:"id"`
	Path        string             `json:"path"`
	Prose       string             `json:"prose"`
	Line        int                `json:"line"`
	EndLine     int                `json:"end_line"`
	Declaration syntax.Declaration `json:"declaration"`
}

func (i Invariant) Reference() string { return i.Path + "#" + i.ID }

type Document struct {
	Invariants []Invariant      `json:"invariants"`
	Syntax     *syntax.Document `json:"-"`
	SHA256     string           `json:"sha256"`
}
type Resolver struct {
	Read   func(string) ([]byte, error)
	Engine *syntax.Engine
	cache  map[string]*Document
	trees  map[string]*syntax.Document
}

func New(read func(string) ([]byte, error), engine *syntax.Engine) *Resolver {
	return &Resolver{Read: read, Engine: engine, cache: map[string]*Document{}, trees: map[string]*syntax.Document{}}
}
func Target(source, ref string) (string, string, error) {
	path, id, ok := strings.Cut(ref, "#")
	if path == "" {
		path = source
	}
	if !ok || !repository.ValidPath(path) || !ID.MatchString(id) || syntax.Language(path) == "" {
		return "", "", fmt.Errorf("invariant reference must be source/path#stable-id: %q", ref)
	}
	return path, id, nil
}
func (r *Resolver) Document(path string) (*Document, error) {
	if d, ok := r.cache[path]; ok {
		return d, nil
	}
	source, err := r.Read(path)
	if err != nil {
		return nil, err
	}
	d, err := Parse(r.Engine, path, source)
	if err != nil {
		return nil, err
	}
	r.cache[path] = d
	return d, nil
}
func (r *Resolver) Resolve(source, ref string) (Invariant, error) {
	path, id, err := Target(source, ref)
	if err != nil {
		return Invariant{}, err
	}
	doc, err := r.Document(path)
	if err != nil {
		return Invariant{}, err
	}
	for _, i := range doc.Invariants {
		if i.ID == id {
			return i, nil
		}
	}
	return Invariant{}, fmt.Errorf("invariant %s#%s does not exist", path, id)
}
func Parse(engine *syntax.Engine, path string, source []byte) (*Document, error) {
	d := &Document{Invariants: []Invariant{}, SHA256: repository.Digest(source)}
	if !bytes.Contains(source, []byte("concave:invariant")) {
		return d, nil
	}
	if engine == nil {
		return nil, fmt.Errorf("invariant discovery requires the syntax engine")
	}
	tree, err := engine.Parse(path, source)
	if err != nil {
		return nil, err
	}
	d.Syntax = tree
	// Only ordinary whole-line // comments declare tooling invariants. Doc/block
	// comments and literal strings remain source, including examples of the grammar.
	comments := map[int]syntax.Capture{}
	for _, c := range tree.Comments {
		if !strings.HasPrefix(c.Text, "//") || strings.HasPrefix(c.Text, "///") || strings.HasPrefix(c.Text, "//!") {
			continue
		}
		start := bytes.LastIndexByte(source[:c.Range.Bytes.Start], 10) + 1
		if len(bytes.TrimSpace(source[start:c.Range.Bytes.Start])) == 0 {
			comments[c.Range.Start.Line] = c
		}
	}
	lines := repository.Lines(string(source))
	seen := map[string]bool{}
	masked := append([]byte{}, source...)
	for _, v := range tree.Comments {
		for j := v.Range.Bytes.Start; j < v.Range.Bytes.End; j++ {
			if masked[j] != 10 {
				masked[j] = 32
			}
		}
	}
	for n := 0; n < len(lines); n++ {
		c, ok := comments[n]
		if !ok {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if !strings.HasPrefix(text, "concave:invariant") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 || fields[0] != "concave:invariant" || !ID.MatchString(fields[1]) {
			return nil, &syntax.SourceError{Path: path, Line: n + 1, Message: "expected concave:invariant ID"}
		}
		id := fields[1]
		if seen[id] {
			return nil, &syntax.SourceError{Path: path, Line: n + 1, Message: fmt.Sprintf("duplicate invariant %s", id)}
		}
		seen[id] = true
		start, end := n, n
		prose := []string{}
		for j := n + 1; j < len(lines); j++ {
			next, ok := comments[j]
			if !ok {
				break
			}
			body := strings.TrimSpace(strings.TrimPrefix(next.Text, "//"))
			if strings.HasPrefix(body, "concave:") {
				break
			}
			prose = append(prose, body)
			end = j
		}
		body := strings.TrimSpace(strings.Join(prose, "\n"))
		if body == "" {
			return nil, &syntax.SourceError{Path: path, Line: start + 1, Message: fmt.Sprintf("invariant %s has no obligation", id)}
		}
		last := comments[end].Range.Bytes.End
		// Comments between adjacent invariant blocks are transparent for attachment.
		candidates := []syntax.Declaration{}
		for _, decl := range tree.Declarations {
			if decl.Range.Bytes.Start >= last && tree.Attachable(masked, last, decl.Range.Bytes.Start) {
				candidates = append(candidates, decl)
			}
		}
		if len(candidates) != 1 {
			return nil, &syntax.SourceError{Path: path, Line: start + 1, Message: fmt.Sprintf("invariant %s must attach to one immediate declaration (found %d)", id, len(candidates))}
		}
		d.Invariants = append(d.Invariants, Invariant{ID: id, Path: path, Prose: body, Line: start + 1, EndLine: end + 1, Declaration: candidates[0]})
		n = end
	}
	return d, nil
}

// Applicable is syntactic containment, never call-graph or trait resolution.
func (d *Document) Applicable(line, column int) []Invariant {
	result := []Invariant{}
	for _, i := range d.Invariants {
		r := i.Declaration.Range
		inside := line >= r.Start.Line+1 && line <= r.End.Line+1
		if column > 0 {
			if line == r.Start.Line+1 && column < r.Start.Column+1 {
				inside = false
			}
			if line == r.End.Line+1 && column >= r.End.Column+1 {
				inside = false
			}
		}
		if inside || line >= i.Line && line <= i.EndLine {
			result = append(result, i)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i].Declaration.Range.Bytes, result[j].Declaration.Range.Bytes
		if a.Start == b.Start {
			return a.End > b.End
		}
		return a.Start < b.Start
	})
	return result
}
func (r *Resolver) Witness(path, symbol string) (string, error) {
	data, err := r.Read(path)
	if err != nil {
		return "missing", err
	}
	if symbol == "" {
		return "file_found", nil
	}
	if syntax.Language(path) == "" {
		return "symbol_not_resolved", nil
	}
	tree := r.trees[path]
	if cached, ok := r.cache[path]; ok {
		tree = cached.Syntax
	}
	if tree == nil {
		tree, err = r.Engine.Parse(path, data)
		if err != nil {
			if syntax.IsUnavailable(err) {
				return "unavailable", err
			}
			return "symbol_not_resolved", nil
		}
		r.trees[path] = tree
	}
	target := strings.ReplaceAll(symbol, "::", "/")
	matches, resolution := 0, "symbol_not_resolved"
	for _, group := range []struct {
		declarations []syntax.Declaration
		resolution   string
	}{{tree.Declarations, "symbol_found"}, {tree.MacroWitnesses, "macro_input_found"}} {
		for _, decl := range group.declarations {
			// Rust implementations scope methods and invariants, but do not
			// introduce another definition of the implemented type.
			if decl.Kind == "impl_item" {
				continue
			}
			parts := []string{}
			for _, parent := range tree.Declarations {
				if syntax.Contains(parent.Range, decl.Range) {
					parts = append(parts, parent.Name)
				}
			}
			parts = append(parts, decl.Name)
			identity := strings.Join(parts, "/")
			if identity == target || strings.HasSuffix(identity, "/"+target) {
				matches++
				resolution = group.resolution
			}
		}
	}
	// A JavaScript test is a call, not a declaration: the reference names its title.
	if matches == 0 {
		for _, c := range tree.TestCases {
			if c.Named(symbol) {
				matches++
				resolution = "test_case_found"
			}
		}
	}
	if matches == 1 {
		return resolution, nil
	}
	if matches > 1 {
		return "symbol_ambiguous", nil
	}
	return "symbol_not_resolved", nil
}
