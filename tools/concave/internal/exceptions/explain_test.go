package exceptions_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
)

func TestExplainBatchSharesAnalysisAndHistory(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, state := range []string{"stale", "valid", "obsolete"} {
			t.Run(map[bool]string{false: "same-file", true: "split-files"}[split]+"/"+state, func(t *testing.T) {
				fixture, sources := batchFixture(t, split)
				repo, config := fixture.Open()
				proposal, err := exceptions.Prepare(repo, config, exceptions.Record, []string{"alpha", "beta"}, exceptions.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if err := exceptions.Write(repo, config, proposal); err != nil {
					t.Fatal(err)
				}
				approved := fixture.Commit()
				for path, source := range sources {
					source = strings.ReplaceAll(source, "second();", "changed_second();")
					source = strings.ReplaceAll(source, "value = 2", "value = 3")
					switch state {
					case "stale":
						source = strings.ReplaceAll(source, "first();", "changed_first();")
					case "obsolete":
						source = strings.Replace(source, namedFence("alpha", "fn first() { first(); }\n"), "fn first() {}\n", 1)
					}
					fixture.Write(path, source)
				}
				before, index := policyBytes(t, fixture), fixture.Git("ls-files", "--stage")
				diff := fixture.Git("diff", "--")
				repo, config = fixture.Open()
				ids := []string{"beta", "alpha"}
				start := repo.Processes
				batch, err := exceptions.Explain(repo, config, ids, "")
				processes := repo.Processes - start
				if err != nil || len(batch) != 2 {
					t.Fatal(batch, err)
				}
				if !reflect.DeepEqual(ids, []string{"beta", "alpha"}) || batch[0].ID != "alpha" || batch[0].Status != state || batch[1].Status != "stale" {
					t.Fatal(batch, ids)
				}
				if batch[1].PreviousSource == nil || batch[1].PreviousSource.Commit != approved {
					t.Fatal(batch[1])
				}
				start = repo.Processes
				for i, id := range []string{"alpha", "beta"} {
					single, err := exceptions.Explain(repo, config, []string{id}, "")
					if err != nil || !reflect.DeepEqual(batch[i], single[0]) {
						t.Fatal("batch changed diagnosis", id, err)
					}
				}
				if processes >= repo.Processes-start {
					t.Fatalf("analysis repeated: batch=%d separate=%d", processes, repo.Processes-start)
				}
				if !bytes.Equal(before, policyBytes(t, fixture)) || index != fixture.Git("ls-files", "--stage") || diff != fixture.Git("diff", "--") {
					t.Fatal("explain wrote source, policy or index")
				}
				if _, err := os.Stat(filepath.Join(fixture.Root, ".git", "index.lock")); !os.IsNotExist(err) {
					t.Fatal("explain retained index lock", err)
				}
			})
		}
	}
}

func TestExplainBatchRejectsInvalidSelectionBeforeAnalysis(t *testing.T) {
	for _, ids := range [][]string{nil, {"read", "read"}, {"read", "missing"}, make([]string, exceptions.MaxChanges+1)} {
		fixture := fixture(t)
		record(t, fixture)
		repo, config := fixture.Open()
		start := repo.Processes
		result, err := exceptions.Explain(repo, config, ids, "")
		if err == nil || result != nil || repo.Processes != start {
			t.Fatal("invalid selection partially analyzed", result, err)
		}
	}
}

func explain(repo *repository.Repo, config *policy.Config, id, rev string) (*exceptions.Explanation, error) {
	result, err := exceptions.Explain(repo, config, []string{id}, rev)
	if err != nil {
		return nil, err
	}
	return result[0], nil
}
