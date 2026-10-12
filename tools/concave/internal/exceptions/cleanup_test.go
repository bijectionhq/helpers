package exceptions_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/exceptions"
)

func TestObsoleteFenceCleanupIsPreviewedAndStaleSafe(t *testing.T) {
	f := fixture(t)
	record(t, f)
	f.Commit()
	source := fenced("fn first() {}\n") + "fn second() {}\n// peer content\n"
	f.Write(path, source)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Refresh, []string{"read"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Changes[0].Action != exceptions.Remove || !strings.Contains(p.SourcePatch, "-// concave:begin") || p.PolicyPatch == "" {
		t.Fatalf("incomplete removal preview: %+v", p)
	}
	current, _ := os.ReadFile(filepath.Join(f.Root, path))
	if string(current) != source {
		t.Fatal("preview mutated source")
	}
	f.Write(path, source+"// peer update\n")
	if err := exceptions.Write(r, c, p); err == nil {
		t.Fatal("stale cleanup applied")
	}
	f.Write(path, source)
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	current, _ = os.ReadFile(filepath.Join(f.Root, path))
	if string(current) != "fn first() {}\nfn second() {}\n// peer content\n" {
		t.Fatalf("cleanup changed code or peer content: %s", current)
	}
	_, c = f.Open()
	if len(c.Exceptions) != 0 {
		t.Fatal("binding remains")
	}
}
