package exceptions

import (
	"concave/internal/policy"
	"concave/internal/testrepo"
	"testing"
)

func TestHistorySearchIsSharedBySelectedFences(t *testing.T) {
	fixture := testrepo.New(t)
	path := "crates/core/lib.rs"
	fence := func(id, code string) string {
		return "// concave:begin " + id + "\n// why: keep ordering\n// witness: tests::read\n" + code + "\n// concave:end " + id + "\n"
	}
	original := fence("alpha", "fn alpha() {}") + fence("beta", "fn beta() {}")
	fixture.Write(path, original)
	fixture.Commit()
	annotations, invalid := policy.Annotations(path, original)
	if len(invalid) != 0 {
		t.Fatal(invalid)
	}
	var selected []policy.Exception
	for _, id := range []string{"alpha", "beta"} {
		selected = append(selected, policy.Exception{ID: id, Path: path, Fence: true, ReplacementSHA256: annotations[id].CodeSHA256()})
	}
	current := fence("alpha", "fn alpha() {}") + fence("beta", "fn beta() { changed(); }")
	fixture.Write(path, current)
	head := fixture.Commit()
	repo, _ := fixture.Open()
	history := newSourceHistory(repo, head, selected)
	first, err := history.previous(selected[0], []byte(current))
	if err != nil || first.Status != "recovered" || first.Examined != 1 {
		t.Fatal(first, err)
	}
	processes, reads := repo.Processes, repo.BlobReads
	second, err := history.previous(selected[1], []byte(current))
	if err != nil || second.Status != "recovered" || second.Examined != 2 || second.Diff == "" {
		t.Fatal(second, err)
	}
	if repo.Processes != processes || repo.BlobReads != reads {
		t.Fatal("second fence rescanned shared history")
	}
	t.Logf("second fence: %d additional Git processes, %d additional blob reads", repo.Processes-processes, repo.BlobReads-reads)
}
