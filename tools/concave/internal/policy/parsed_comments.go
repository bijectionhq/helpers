package policy

import (
	"fmt"
	"strings"

	"concave/internal/syntax"
)

// ParsedComments maps the parser's standalone comments to zero-based lines: a
// line comment with only whitespace before it on its line, or a JSX expression
// container holding only a block comment, alone on its lines, whose lines map
// like an MDX comment. Other block comments never carry annotations. Text in
// strings, templates, regular expressions and JSX is never a comment.
func ParsedComments(text string, facts syntax.Comments) (map[int]string, error) {
	// The grammar recovers locally from syntax it does not support. Only an
	// error touching a line that mentions an annotation leaves it undecided.
	for _, e := range facts.Errors {
		start := strings.LastIndexByte(text[:e.Start], '\n') + 1
		end := len(text)
		if i := strings.IndexByte(text[e.End:], '\n'); i >= 0 {
			end = e.End + i
		}
		if i := strings.Index(text[start:end], "concave:"); i >= 0 {
			return nil, fmt.Errorf("source does not parse near an annotation at line %d", strings.Count(text[:start+i], "\n")+1)
		}
	}
	out := map[int]string{}
	alone := func(start, end int) bool {
		before := text[strings.LastIndexByte(text[:start], '\n')+1 : start]
		after := text[end:]
		if i := strings.IndexByte(after, '\n'); i >= 0 {
			after = after[:i]
		}
		return strings.TrimSpace(before) == "" && strings.TrimSpace(after) == ""
	}
	for _, c := range facts.Comments {
		body := text[c.Start:c.End]
		line := strings.Count(text[:c.Start], "\n")
		if strings.HasPrefix(body, "//") {
			if strings.TrimSpace(text[strings.LastIndexByte(text[:c.Start], '\n')+1:c.Start]) == "" {
				out[line] = strings.TrimSpace(body[2:])
			}
			continue
		}
		for _, jsx := range facts.JSX {
			if jsx.Start < c.Start && c.End < jsx.End && strings.TrimSpace(text[jsx.Start+1:c.Start]) == "" && strings.TrimSpace(text[c.End:jsx.End-1]) == "" && alone(jsx.Start, jsx.End) {
				for i, part := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(body, "/*"), "*/"), "\n") {
					out[line+i] = strings.TrimSpace(part)
				}
				break
			}
		}
	}
	return out, nil
}
