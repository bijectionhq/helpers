package output

import (
	"concave/internal/policy"
	"fmt"
	"io"
)

// RDJSON is reviewdog diagnostic format. This is a projection of the full set of
// findings, not another policy engine. It never filters by changed lines, turns
// failures into warnings, or emits edits that bypass guarded recovery.
// concave:invariant complete-diagnostics
// Successful output includes one error per input finding with its rule and known
// location. Display limits cannot suppress findings, and output contains no edits.
func RDJSON(w io.Writer, findings []policy.Finding) error {
	type position struct {
		Line int `json:"line"`
	}
	type span struct {
		Start position `json:"start"`
		End   position `json:"end"`
	}
	type location struct {
		Path  string `json:"path"`
		Range *span  `json:"range,omitempty"`
	}
	type code struct {
		Value string `json:"value"`
	}
	type diagnostic struct {
		Message  string   `json:"message"`
		Location location `json:"location"`
		Severity string   `json:"severity"`
		Code     code     `json:"code"`
	}
	diagnostics := make([]diagnostic, 0, len(findings))
	for _, f := range findings {
		d := diagnostic{Message: f.Message, Location: location{Path: f.Path}, Severity: "ERROR", Code: code{Value: f.Rule}}
		if f.Line > 0 {
			d.Location.Range = &span{Start: position{f.Line}, End: position{max(f.Line, f.EndLine)}}
		}
		if f.Exception != "" {
			d.Message += fmt.Sprintf(" (exception %s)", f.Exception)
		}
		diagnostics = append(diagnostics, d)
	}
	return JSON(w, struct {
		Diagnostics []diagnostic `json:"diagnostics"`
		Source      struct {
			Name string `json:"name"`
		} `json:"source"`
	}{Diagnostics: diagnostics, Source: struct {
		Name string `json:"name"`
	}{Name: "concave"}})
}
