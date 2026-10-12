package analysis

import (
	"testing"

	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestReviewSourcesPreserveVersionsAndRejectMixedInput(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	path := "crates/core/lib.rs"
	if _, err = snapshot.Read(path); err != nil {
		t.Fatal(err)
	}
	expected := snapshot.Inputs[path]
	collector := newReviewSources()
	for _, version := range []string{"baseline", "candidate", "candidate"} {
		if err = collector.add(version, path, snapshot, expected, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(collector.sources) != 2 || len(collector.blobs) != 1 || collector.bytes != int(expected.Size) {
		t.Fatal("source versions or shared body lost", collector)
	}
	changed := *expected
	changed.OID = "different"
	if err = collector.add("candidate", path, snapshot, &changed, true); !repository.IsStale(err) {
		t.Fatal("deduplication bypassed source agreement", err)
	}
	if err = collector.add("candidate", "missing.rs", snapshot, nil, true); err != nil {
		t.Fatal(err)
	}
	if collector.sources[2].Unavailable == "" {
		t.Fatal("invented missing body")
	}
}

func TestReviewSourcesEnforceTheUniqueBodyBudget(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	collector := newReviewSources()
	collector.bytes = repository.MaxOutput / 2
	if err = collector.add("candidate", "crates/core/lib.rs", snapshot, nil, false); err == nil {
		t.Fatal("source budget exceeded")
	}
}

func TestContractResolutionDefersFreshnessToAcceptance(t *testing.T) {
	fixture := testrepo.New(t)
	repo, config := fixture.Open()
	snapshot, err := repository.OpenSnapshot(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	path := "crates/core/lib.rs"
	if _, err = snapshot.Read(path); err != nil {
		t.Fatal(err)
	}
	resolver, err := newContractResolver(snapshot, policy.NewSession(repo, config))
	if err != nil {
		t.Fatal(err)
	}
	reads := repo.Reads
	for range 2 {
		if _, _, err = resolver.Resolve([]contractSource{resolver.Source(path)}, contractOptions{Context: true}); err != nil {
			t.Fatal(err)
		}
	}
	if repo.Reads != reads {
		t.Fatal("resolver repeated the acceptance sweep", repo.Reads, reads)
	}
	fixture.Write(path, "fn changed() {}\n")
	if _, err = snapshot.Verify(); !repository.IsStale(err) {
		t.Fatal("final acceptance failed to reject changed context", err)
	}
}
