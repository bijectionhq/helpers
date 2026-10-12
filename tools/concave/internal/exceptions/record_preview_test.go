package exceptions_test

import (
	"strings"
	"testing"

	"concave/internal/exceptions"
)

func TestNewFencePreviewSelectsOnlyCompleteBoundedHunks(t *testing.T) {
	for _, scope := range []string{"complete", "crossing", "partial-selection"} {
		t.Run(scope, func(t *testing.T) {
			f := fixture(t)
			code := fenced("fn first() { read(); }\n") + "fn second() {}\n\nfn unrelated() {}\n"
			if scope == "crossing" {
				code = fenced("fn first() { read(); }\n") + "fn second() { changed(); }\n"
			}
			f.Write(path, code)
			o := exceptions.Options{Path: path}
			if scope == "partial-selection" {
				o.Hunks = []string{"omitted-current-hunks"}
			}
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, "record", []string{"read"}, o)
			if scope == "complete" {
				if err != nil || p.Status != "preview" || len(p.Changes[0].Hunks) != 1 || strings.Contains(p.Changes[0].SourceDiff, "unrelated") || len(c.Exceptions) != 0 {
					t.Fatal(p, err)
				}
			} else if err == nil {
				t.Fatal("invalid selection accepted", p)
			} else if scope == "crossing" && (!strings.Contains(err.Error(), path+":") || !strings.Contains(err.Error(), "hunk ") || !strings.Contains(err.Error(), "fence read")) {
				t.Fatal("crossing boundary has no actionable location", err)
			}
		})
	}
}
