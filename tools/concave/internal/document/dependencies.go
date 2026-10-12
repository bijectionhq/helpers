package document

import (
	"bytes"
	"concave/internal/repository"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Problem is a document finding. Import names the unresolved local import.
type Problem struct{ Kind, Path, Import, Message string }

// Graph is one traversal. Dependencies holds each root's transitive imports.
// Inputs holds, for every parsed document, each path whose bytes or absence can
// change its parse or resolution: itself, every probed import candidate, and the
// same for each document it imports. That set is the document's own dependency
// identity, so an edit elsewhere never changes it.
type Graph struct {
	Dependencies map[string][]string
	Inputs       map[string][]string
	Problems     []Problem
}

// Dependencies parses one frontier at a time across all roots. Shared partials
// and cycles are visited once; each root still receives its full transitive
// dependency set. Read owns source capture, including absent extension probes.
func (e *Engine) Dependencies(roots []Source, read func(string) ([]byte, error)) (Graph, error) {
	edges := map[string][]string{}
	probes := map[string][]string{}
	problems := []Problem{}
	seen := map[string]bool{}
	queue := []Source{}
	for _, root := range roots {
		if !seen[root.Path] {
			seen[root.Path] = true
			queue = append(queue, root)
		}
	}
	type resolution struct {
		path   string
		data   []byte
		probes []string
		err    error
	}
	resolved := map[string]resolution{}
	for len(queue) > 0 {
		frontier := queue
		queue = nil
		if err := e.Batch(frontier); err != nil {
			return Graph{}, err
		}
		for _, source := range frontier {
			facts, err := e.Parse(source.Path, source.Source)
			if err != nil {
				return Graph{}, err
			}
			if facts.Error != "" {
				problems = append(problems, Problem{Kind: "invalid_document", Path: source.Path, Message: facts.Error})
				continue
			}
			for _, imported := range facts.Imports {
				dependency, local, err := ImportPath(source.Path, imported.Specifier)
				if err != nil {
					problems = append(problems, Problem{Kind: "invalid_document", Path: source.Path, Import: imported.Specifier, Message: err.Error()})
					continue
				}
				if !local {
					continue
				}
				result, ok := resolved[dependency]
				if !ok {
					if len(resolved) >= 4096 {
						return Graph{}, fmt.Errorf("document dependency count exceeds bound")
					}
					result.path, result.data, result.probes, result.err = resolveImport(dependency, read)
					resolved[dependency] = result
				}
				probes[source.Path] = append(probes[source.Path], result.probes...)
				if result.err != nil {
					if repository.IsStale(result.err) {
						return Graph{}, result.err
					}
					problems = append(problems, Problem{Kind: "missing_document_import", Path: source.Path, Import: dependency, Message: dependency + ": " + result.err.Error()})
					continue
				}
				edges[source.Path] = append(edges[source.Path], result.path)
				if Supports(result.path) && !seen[result.path] {
					seen[result.path] = true
					queue = append(queue, Source{result.path, string(result.data)})
				}
			}
		}
	}
	closure := func(start string) []string {
		pending := append([]string{}, edges[start]...)
		visited := map[string]bool{start: true}
		dependencies := []string{}
		for len(pending) > 0 {
			current := pending[0]
			pending = pending[1:]
			if visited[current] {
				continue
			}
			visited[current] = true
			dependencies = append(dependencies, current)
			pending = append(pending, edges[current]...)
		}
		sort.Strings(dependencies)
		return dependencies
	}
	graph := Graph{Dependencies: map[string][]string{}, Inputs: map[string][]string{}, Problems: problems}
	for _, root := range roots {
		graph.Dependencies[root.Path] = closure(root.Path)
	}
	for document := range seen {
		inputs := map[string]bool{document: true}
		for _, node := range append(closure(document), document) {
			inputs[node] = true
			for _, probe := range probes[node] {
				inputs[probe] = true
			}
		}
		list := make([]string, 0, len(inputs))
		for input := range inputs {
			list = append(list, input)
		}
		sort.Strings(list)
		graph.Inputs[document] = list
	}
	return graph, nil
}

// suffixProbes are the suffixes an extensionless local import tries, in order.
var suffixProbes = []string{".mdx", ".md", ".tsx", ".ts", ".jsx", ".js", "/index.tsx", "/index.ts", "/index.js"}

// Reach decides which documents can resolve a local import to any added
// target. An import's specifier is a JavaScript string literal, so every quoted
// literal in a document's raw bytes is decoded and resolved as one: a match
// against a target, or a stem the suffix probes extend to it, admits the
// document. Literals in prose and code samples only add candidates.
type Reach struct{ paths map[string]bool }

func (r *Reach) Add(target string) {
	if r.paths == nil {
		r.paths = map[string]bool{}
	}
	r.paths[target] = true
	for _, suffix := range suffixProbes {
		if stem, ok := strings.CutSuffix(target, suffix); ok && stem != "" {
			r.paths[stem] = true
		}
	}
}

// May reports whether the document at owner, with source, can import a target.
func (r *Reach) May(owner string, source []byte) bool {
	for i, c := range source {
		if c != '"' && c != '\'' {
			continue
		}
		specifier, ok := stringLiteral(source[i:])
		if !ok {
			continue
		}
		resolved, local, err := ImportPath(owner, specifier)
		if err == nil && local && r.paths[resolved] {
			return true
		}
	}
	return false
}

// singleEscapes are the one-character JavaScript string escapes.
var singleEscapes = map[rune]rune{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}

// stringLiteral decodes the JavaScript string literal opening at source[0]
// into UTF-16 code units, as the language defines its value, and then into
// UTF-8. False means no literal valid in module code opens there.
func stringLiteral(source []byte) (string, bool) {
	quote := rune(source[0])
	var units []uint16
	unit := func(r rune) {
		if r <= 0xFFFF {
			units = append(units, uint16(r))
		} else {
			units = utf16.AppendRune(units, r)
		}
	}
	hex := func(digits []byte) (rune, bool) {
		n, err := strconv.ParseUint(string(digits), 16, 32)
		return rune(n), err == nil && n <= unicode.MaxRune
	}
	for i := 1; i < len(source); {
		r, size := utf8.DecodeRune(source[i:])
		i += size
		switch {
		case r == quote:
			return string(utf16.Decode(units)), true
		case r == '\n' || r == '\r':
			return "", false
		case r != '\\':
			unit(r)
			continue
		case i == len(source):
			return "", false
		}
		r, size = utf8.DecodeRune(source[i:])
		i += size
		if e, ok := singleEscapes[r]; ok {
			unit(e)
			continue
		}
		switch r {
		case '0':
			if i < len(source) && source[i] >= '0' && source[i] <= '9' {
				return "", false
			}
			unit(0)
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return "", false
		case 'x', 'u':
			var digits []byte
			switch {
			case r == 'u' && i < len(source) && source[i] == '{':
				end := bytes.IndexByte(source[i:], '}')
				if end < 2 {
					return "", false
				}
				digits, i = source[i+1:i+end], i+end+1
			case r == 'x' && i+2 <= len(source):
				digits, i = source[i:i+2], i+2
			case r == 'u' && i+4 <= len(source):
				digits, i = source[i:i+4], i+4
			default:
				return "", false
			}
			code, ok := hex(digits)
			if !ok {
				return "", false
			}
			unit(code)
		case '\r':
			if i < len(source) && source[i] == '\n' {
				i++
			}
		case '\n', '\u2028', '\u2029':
			// A line continuation contributes nothing.
		default:
			unit(r)
		}
	}
	return "", false
}

func resolveImport(dependency string, read func(string) ([]byte, error)) (string, []byte, []string, error) {
	tried := []string{dependency}
	data, err := read(dependency)
	if err == nil || repository.IsStale(err) || path.Ext(dependency) != "" {
		return dependency, data, tried, err
	}
	matches := 0
	var chosen []byte
	result := dependency
	for _, suffix := range suffixProbes {
		tried = append(tried, dependency+suffix)
		data, probeErr := read(dependency + suffix)
		if repository.IsStale(probeErr) {
			return "", nil, tried, probeErr
		}
		if probeErr == nil {
			matches++
			chosen = data
			result = dependency + suffix
		}
	}
	if matches == 1 {
		return result, chosen, tried, nil
	}
	if matches > 1 {
		return "", nil, tried, fmt.Errorf("ambiguous local import %s", dependency)
	}
	return dependency, nil, tried, err
}
