package policy

import (
	"concave/internal/repository"
	"strings"
)

// AnnotationFree removes only recognized standalone Concave comment ranges.
// Equality of the remaining bytes is descriptive, never an approval rule.
func AnnotationFree(path, text string) (string, bool) {
	return (&Session{}).AnnotationFree(path, text)
}
func (c *Session) AnnotationFree(path, text string) (string, bool) {
	annotations, bad := c.Annotations(path, text)
	if len(bad) > 0 {
		return "", false
	}
	comments, err := c.commentLines(path, text)
	if err != nil {
		return "", false
	}
	lines := repository.Lines(text)
	skip := map[int]bool{}
	for _, a := range annotations {
		for i := a.Start; i < a.End; i++ {
			skip[i] = i < a.CodeStart || i >= a.CodeEnd
		}
	}
	for i := 0; i < len(lines); i++ {
		comment, ok := comments[i]
		if !ok || !strings.HasPrefix(comment, "concave:invariant ") {
			continue
		}
		fields := strings.Fields(comment)
		if len(fields) != 2 || !idPattern.MatchString(fields[1]) {
			return "", false
		}
		skip[i] = true
		for i+1 < len(lines) {
			next, ok := comments[i+1]
			if !ok || strings.HasPrefix(next, "concave:") {
				break
			}
			i++
			skip[i] = true
		}
	}
	var out strings.Builder
	for i, line := range lines {
		if !skip[i] {
			out.WriteString(line)
		}
	}
	return out.String(), true
}
