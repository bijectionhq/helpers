package exceptions_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
)

// Removal is an explicit, previewed retirement. A binding whose fence remains
// in source is refused with its next action; once the fence is gone the
// preview names what the removal leaves, and the write deletes only that
// path's binding file.
func TestRemoveRefusesAFencedBindingAndReportsWhatRemains(t *testing.T) {
	f, sources := batchFixture(t, true)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	r, c = f.Open()
	if _, err := exceptions.Prepare(r, c, exceptions.Remove, []string{"alpha"}, exceptions.Options{}); err == nil || !strings.Contains(err.Error(), "still fenced at "+path+":1") || !strings.Contains(err.Error(), "concave exception refresh alpha") {
		t.Fatal("fenced removal accepted or unexplained:", err)
	}
	if _, err := exceptions.Prepare(r, c, exceptions.Remove, []string{"missing"}, exceptions.Options{}); err == nil {
		t.Fatal("unknown binding removal accepted")
	}
	if _, err := exceptions.Prepare(r, c, exceptions.Remove, []string{"beta"}, exceptions.Options{Path: path}); err == nil {
		t.Fatal("removal accepted a path option")
	}
	// Keep the code, drop the fence: the divergence remains and is reported.
	kept := []string{}
	for _, line := range strings.SplitAfter(sources[path], "\n") {
		if !strings.HasPrefix(line, "//") {
			kept = append(kept, line)
		}
	}
	f.Write(path, strings.Join(kept, ""))
	r, c = f.Open()
	p, err = exceptions.Prepare(r, c, exceptions.Remove, []string{"alpha"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || p.Changes[0].Next != nil || len(p.Changes[0].Remaining) == 0 || p.Changes[0].Remaining[0].Rule != "source/covered" || len(p.BindingPaths) != 1 || p.BindingPaths[0] != path {
		t.Fatalf("removal preview: %+v", p.Changes[0])
	}
	if !strings.Contains(p.PolicyPatch, "deleted file mode") {
		t.Fatal("last binding of a path must delete its file:", p.PolicyPatch)
	}
	// Restoring upstream source leaves nothing; the write retires the binding.
	f.Write(path, "fn first() {}\nfn middle() {}\nfn second() {}\nfn last() {}\n\nfn unrelated() {}\n")
	if err := exceptions.Write(r, c, p); !repository.IsStale(err) {
		t.Fatal("removal preview survived a source change:", err)
	}
	r, c = f.Open()
	p, err = exceptions.Prepare(r, c, exceptions.Remove, []string{"alpha"}, exceptions.Options{})
	if err != nil || len(p.Changes[0].Remaining) != 0 {
		t.Fatal(p, err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Root, policy.BindingPath(path))); !os.IsNotExist(err) {
		t.Fatal("binding file kept after its last binding was removed", err)
	}
	_, c = f.Open()
	if ids := bindingIDs(t, c); len(ids) != 1 || ids[0] != "beta" {
		t.Fatal("removal touched another path's binding", ids)
	}
	if err := exceptions.ValidateRecorded(r, c, p); err != nil {
		t.Fatal("recorded removal does not validate:", err)
	}
}

// Removing a path's last binding deletes its committed binding file; a later
// record on that path creates the file again.
func TestRecordAfterRemovingEveryBindingOfAPath(t *testing.T) {
	f, sources := batchFixture(t, true)
	r, c := f.Open()
	p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	f.Commit()
	f.Write(path, "fn first() {}\nfn middle() {}\nfn second() {}\nfn last() {}\n\nfn unrelated() {}\n")
	r, c = f.Open()
	if p, err = exceptions.Prepare(r, c, exceptions.Remove, []string{"alpha"}, exceptions.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.Root, policy.BindingPath(path))); !os.IsNotExist(err) {
		t.Fatal("binding file kept", err)
	}
	f.Write(path, strings.Replace(sources[path], namedFence("alpha", "fn first() { first(); }\n"), namedFence("gamma", "fn first() { gamma(); }\n"), 1))
	r, c = f.Open()
	if p, err = exceptions.Prepare(r, c, exceptions.Record, []string{"gamma"}, exceptions.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := exceptions.Write(r, c, p); err != nil {
		t.Fatal("recreating a removed binding file:", err)
	}
	_, c = f.Open()
	if ids := bindingIDs(t, c); len(ids) != 2 || ids[0] != "beta" || ids[1] != "gamma" {
		t.Fatal(ids)
	}
}
