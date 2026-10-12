package syntax

import "fmt"

// SourceError keeps an actual source location separate from diagnostic prose.
// Callers can locate a referenced declaration without parsing an error string.
type SourceError struct {
	Path    string
	Line    int
	Message string
}

func (e *SourceError) Error() string { return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message) }
