package surface_test

import (
	"concave/internal/repository"
	"concave/internal/surface"
	"testing"
)

func TestCallerEvidenceRejectsChangedBackContractSource(t *testing.T) {
	f := fixture(t)
	original := "import {renamed} from './entry.js'; renamed('x');\n"
	f.Write("use.ts", original)
	repo, _ := f.Open()
	report, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{"entry.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	f.Write("use.ts", original+"// concurrent edit\n")
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = snapshot.Read("use.ts"); err != nil {
		t.Fatal(err)
	}
	f.Write("use.ts", original)
	if err = surface.Validate(repo, report); err != nil {
		t.Fatal(err)
	}
	if err = surface.ValidateObserved(report, snapshot.Inputs); err == nil {
		t.Fatal("mixed source versions accepted after bytes changed back")
	}
}
