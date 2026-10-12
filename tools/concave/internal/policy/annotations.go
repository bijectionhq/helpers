package policy

import (
	"fmt"
	"path/filepath"
	"strings"

	"concave/internal/contracts"
	"concave/internal/document"
	"concave/internal/repository"
	"concave/internal/syntax"
)

// Explanation has one author: a source annotation, or a policy entry when the
// file cannot carry annotations. These are witness references, never test results.
type Explanation struct {
	Why        string   `json:"why"`
	Witnesses  []string `json:"witnesses"`
	RemoveWhen string   `json:"remove_when,omitempty"`
	Preserves  []string `json:"preserves,omitempty"`
}

func (m Explanation) Validate() error {
	seenPreserves := map[string]bool{}
	for _, ref := range m.Preserves {
		if _, _, err := contracts.Target("source.rs", ref); err != nil {
			return err
		}
		if seenPreserves[ref] {
			return fmt.Errorf("duplicate preserved invariant %s", ref)
		}
		seenPreserves[ref] = true
	}
	if strings.TrimSpace(m.Why) == "" || len(m.Witnesses) == 0 {
		return fmt.Errorf("require why and at least one witness")
	}
	seen := map[string]bool{}
	for _, w := range m.Witnesses {
		if strings.TrimSpace(w) == "" || seen[w] {
			return fmt.Errorf("witnesses must be nonempty and unique")
		}
		seen[w] = true
		if _, _, err := WitnessTarget("source.rs", w); err != nil {
			return err
		}
	}
	return nil
}
func WitnessTarget(source, witness string) (string, string, error) {
	path, symbol, hasSymbol := strings.Cut(witness, "::")
	// tests::name is local to the annotated file. Explicit paths use path::symbol.
	if hasSymbol && !strings.Contains(path, "/") && !strings.Contains(path, ".") {
		path = source
		symbol = witness
	}
	if !repository.ValidPath(path) || hasSymbol && symbol == "" {
		return "", "", fmt.Errorf("invalid witness reference %q", witness)
	}
	return path, symbol, nil
}
func SupportsAnnotations(path string) bool {
	switch filepath.Ext(path) {
	case ".md", ".mdx", ".rs", ".go", ".ts", ".mts", ".cts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".toml", ".proto":
		return true
	}
	return false
}

// Annotation ranges are zero-based and end-exclusive, including the markers.
// Code is the fenced body after the metadata and its blank separator lines.
type Annotation struct {
	ID          string      `json:"id"`
	Start       int         `json:"start"`
	End         int         `json:"end"`
	Explanation Explanation `json:"explanation"`
	Whole       string      `json:"-"`
	Code        string      `json:"-"`
	// CodeStart and CodeEnd delimit Code's lines; a marker wrapped by a
	// formatter occupies two lines.
	CodeStart int `json:"-"`
	CodeEnd   int `json:"-"`
}

// CodeSHA256 is a fenced binding's replacement digest. It binds the code the
// fence explains, never its markers, metadata or separators, so formatter
// re-wrapping of the explanation cannot invalidate an approval.
func (a Annotation) CodeSHA256() string {
	return repository.Digest([]byte(a.Code))
}

var metadataFields = map[string]bool{"why": true, "witness": true, "remove-when": true, "preserves": true}

// StandaloneComments lexically recognizes whole-line comments outside literals
// and block comments: # in TOML, // in Rust, Go and protobuf. Markdown, MDX,
// TypeScript and JavaScript comments come only from their parsers (Session).
func StandaloneComments(path, text string) map[int]string {
	if document.Supports(path) || syntax.CommentLanguage(path) != "" {
		return map[int]string{}
	}
	if filepath.Ext(path) == ".toml" {
		return tomlComments(text)
	}
	out := map[int]string{}
	line := 0
	lineStart := 0
	rust := filepath.Ext(path) == ".rs"
	for i := 0; i < len(text); {
		if text[i] == '\n' {
			line++
			i++
			lineStart = i
			continue
		}
		if strings.HasPrefix(text[i:], "//") {
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			end += i
			// Rust documentation belongs to the annotated item, not to
			// formatter-wrapped metadata from an ordinary // comment.
			documentation := rust && (strings.HasPrefix(text[i:], "///") || strings.HasPrefix(text[i:], "//!"))
			if !documentation && strings.TrimSpace(text[lineStart:i]) == "" {
				out[line] = strings.TrimSpace(text[i+2 : end])
			}
			i = end
			continue
		}
		if strings.HasPrefix(text[i:], "/*") {
			depth := 1
			i += 2
			for i < len(text) && depth > 0 {
				if rust && strings.HasPrefix(text[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(text[i:], "*/") {
					depth--
					i += 2
				} else {
					if text[i] == '\n' {
						line++
						lineStart = i + 1
					}
					i++
				}
			}
			continue
		}
		if rust && text[i] == 'r' {
			j := i + 1
			for j < len(text) && text[j] == '#' {
				j++
			}
			if j < len(text) && text[j] == '"' {
				close := "\"" + strings.Repeat("#", j-i-1)
				end := strings.Index(text[j+1:], close)
				if end < 0 {
					end = len(text)
				} else {
					end = j + 1 + end + len(close)
				}
				for i < end {
					if text[i] == '\n' {
						line++
						lineStart = i + 1
					}
					i++
				}
				continue
			}
		}
		quote := text[i]
		quoted := quote == '"' || quote == '`' || quote == '\''
		if rust && quote == '\'' {
			// Lifetimes have no closing quote. A character literal closes on this line.
			j := i + 1
			for j < len(text) && text[j] != '\n' && j < i+16 {
				if text[j] == '\\' {
					j += 2
					continue
				}
				if text[j] == '\'' {
					break
				}
				j++
			}
			quoted = j < len(text) && text[j] == '\'' && j < i+16
		}
		if quoted {
			i++
			for i < len(text) {
				if text[i] == '\\' && (quote != '`' || filepath.Ext(path) != ".go") {
					i++
					if i < len(text) {
						if text[i] == '\n' {
							line++
							lineStart = i + 1
						}
						i++
					}
					continue
				}
				if text[i] == quote {
					i++
					break
				}
				if text[i] == '\n' {
					line++
					lineStart = i + 1
				}
				i++
			}
			continue
		}
		i++
	}
	return out
}

// Annotations reads fences without a syntax engine or documentation parser;
// TypeScript, JavaScript and Markdown sources need a Session.
func Annotations(path, text string) (map[string]Annotation, []Finding) {
	return (&Session{}).Annotations(path, text)
}
func annotations(path, text string, comments map[int]string) (map[string]Annotation, []Finding) {
	out := map[string]Annotation{}
	bad := []Finding{}
	if !SupportsAnnotations(path) {
		return out, bad
	}
	lines := repository.Lines(text)
	active := ""
	start := 0
	seen := map[string]bool{}
	problem := func(i int, id, msg string) {
		bad = append(bad, Finding{Kind: "invalid_fence", Path: path, Line: i + 1, EndLine: i + 1, Rule: "exception/valid", Exception: id, Subject: Subject(strings.TrimSpace(lines[i])), Message: msg})
	}
	metadataStart := 0
	for i := 0; i < len(lines); i++ {
		comment, ok := comments[i]
		if !ok || !strings.HasPrefix(comment, "concave:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(comment, "concave:"))
		if len(fields) > 0 && fields[0] == "invariant" {
			continue
		}
		marker := i
		// Comment wrapping (rustfmt wrap_comments) moves a long ID onto the next
		// comment line. That line holds exactly the one ID token.
		if len(fields) == 1 && (fields[0] == "begin" || fields[0] == "end") {
			if next, ok := comments[i+1]; ok && idPattern.MatchString(next) {
				fields = append(fields, next)
				i++
			}
		}
		if len(fields) != 2 || (fields[0] != "begin" && fields[0] != "end") || !idPattern.MatchString(fields[1]) {
			problem(i, "", "expected a standalone concave:begin|end ID comment")
			continue
		}
		id := fields[1]
		if fields[0] == "begin" {
			if active != "" || seen[id] {
				problem(marker, id, "nested or duplicate fence")
				continue
			}
			seen[id] = true
			active = id
			start = marker
			metadataStart = i + 1
			continue
		}
		if active != id || active == "" {
			problem(marker, id, "unmatched end fence")
			continue
		}
		a := Annotation{ID: id, Start: start, End: i + 1, Whole: strings.Join(lines[start:i+1], ""), CodeEnd: marker}
		// Metadata is the run of standalone comments after the begin marker.
		// Each is an empty separator, a field label (why, witness, remove-when,
		// preserves) or a continuation line of the open field. A separator
		// closes the field. Metadata ends at the first other line: a blank or
		// code line, Rust documentation, or an ordinary comment while no field
		// is open (at the start or after a separator). Blank lines after the
		// metadata belong to neither it nor the code. Formatter re-wrapping
		// moves only continuation boundaries inside a separated field: prose
		// joins continuations with one space, and references (which never
		// contain whitespace) drop all whitespace the wrap introduced.
		values := map[string]string{}
		key := ""
		keyLine := metadataStart
		finishField := func() {
			if key == "" {
				return
			}
			value := values[key]
			if key == "witness" {
				value = a.Explanation.Witnesses[len(a.Explanation.Witnesses)-1]
			}
			if key == "preserves" {
				value = a.Explanation.Preserves[len(a.Explanation.Preserves)-1]
			}
			if strings.TrimSpace(value) == "" {
				problem(keyLine, id, "empty annotation field "+key)
			}
			key = ""
		}
		reference := func(refs []string, more string) {
			refs[len(refs)-1] = strings.Join(strings.Fields(refs[len(refs)-1]+more), "")
		}
		codeStart := metadataStart
		for j := metadataStart; j < marker; j++ {
			v, ok := comments[j]
			if !ok {
				break
			}
			if v == "" {
				finishField()
				codeStart = j + 1
				continue
			}
			name, value, labelled := strings.Cut(v, ":")
			labelled = labelled && idPattern.MatchString(name) && (value == "" || strings.HasPrefix(value, " "))
			switch {
			case labelled && metadataFields[name]:
				// A formatter may put the value entirely on following comment
				// lines. Validate a field after all its continuations are read.
				finishField()
				if _, duplicate := values[name]; duplicate && name != "witness" && name != "preserves" {
					problem(j, id, "duplicate annotation field "+name)
				}
				if name == "witness" {
					a.Explanation.Witnesses = append(a.Explanation.Witnesses, "")
					reference(a.Explanation.Witnesses, value)
				} else if name == "preserves" {
					a.Explanation.Preserves = append(a.Explanation.Preserves, "")
					reference(a.Explanation.Preserves, value)
				} else {
					values[name] = strings.TrimSpace(value)
				}
				key = name
				keyLine = j
			case key == "why" || key == "remove-when":
				// Prose may wrap a "word:" to the start of a line.
				values[key] = strings.TrimSpace(values[key] + " " + v)
			case labelled:
				// A reference has no whitespace to wrap at, so a label-shaped
				// line after one, or with no open field, is an unknown field.
				problem(j, id, "unknown annotation field "+name)
				finishField()
			case key == "witness":
				reference(a.Explanation.Witnesses, v)
			case key == "preserves":
				reference(a.Explanation.Preserves, v)
			}
			if key == "" {
				break
			}
			// Re-flowing a field without a separator can push the next field's
			// label into this paragraph. Refuse it rather than lose that field.
			for _, word := range strings.Fields(v)[1:] {
				if label, ok := strings.CutSuffix(word, ":"); ok && metadataFields[label] {
					problem(j, id, "field "+label+" wrapped into the preceding text; separate fields with an empty comment line")
					break
				}
			}
			codeStart = j + 1
		}
		finishField()
		for codeStart < marker && strings.TrimSpace(lines[codeStart]) == "" {
			codeStart++
		}
		a.Explanation.Why = values["why"]
		a.Explanation.RemoveWhen = values["remove-when"]
		a.CodeStart = codeStart
		a.Code = strings.Join(lines[codeStart:marker], "")
		if err := a.Explanation.Validate(); err != nil {
			problem(start, id, err.Error())
		}
		out[id] = a
		active = ""
	}
	if active != "" {
		problem(start, active, "unclosed fence")
	}
	return out, bad
}
