package repository_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"concave/internal/repository"
)

func TestHunkIdentityPreservesEncodingAndWholeSourceBinding(t *testing.T) {
	before, after := []byte("one\ntwo\n"), []byte("ONE\ntwo\nthree\n")
	hunks := []repository.Hunk{
		{Start: 0, End: 1, CurrentStart: 0, CurrentEnd: 1},
		{Start: 2, End: 2, CurrentStart: 2, CurrentEnd: 3},
	}
	identified := repository.Identify("core.rs", before, after, append([]repository.Hunk{}, hunks...))
	for i, want := range []string{"3f44012c4ece3a0a780a", "6fa60989c1f9f443fddf"} {
		if identified[i].ID != want || identified[i].Path != "core.rs" {
			t.Fatal(identified)
		}
	}
	// A change outside a selected span must still invalidate every old hunk ID.
	changed := repository.Identify("core.rs", before, []byte("ONE\nTWO\nthree\n"), append([]repository.Hunk{}, hunks...))
	for i := range changed {
		if changed[i].ID == identified[i].ID {
			t.Fatal("unbound source change", changed)
		}
	}
	if repository.Identify("core.rs", nil, nil, nil) != nil {
		t.Fatal("empty hunk slice changed")
	}
}

func TestDiffBuffersKeepHunksFilesAndMissingNewlinesIndependent(t *testing.T) {
	var patch strings.Builder
	const count = 512
	fmt.Fprintf(&patch, "diff --git a/first.rs b/first.rs\n--- a/first.rs\n+++ b/first.rs\n@@ -1,%d +1,%d @@\n", count, count)
	before, after := strings.Repeat("old é\r\n", count), strings.Repeat("new é\r\n", count)
	for i := 0; i < count; i++ {
		patch.WriteString("-old é\r\n")
	}
	for i := 0; i < count; i++ {
		patch.WriteString("+new é\r\n")
	}
	patch.WriteString("@@ -514 +514 @@\n-before\n\\ No newline at end of file\n+after\n\\ No newline at end of file\n")
	patch.WriteString("diff --git a/second.rs b/second.rs\n--- a/second.rs\n+++ b/second.rs\n@@ -0,0 +1 @@\n+next\n")
	got, err := repository.ParseDiff([]byte(patch.String()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]repository.Hunk{
		"first.rs": {
			{Path: "first.rs", Start: 0, End: count, CurrentStart: 0, CurrentEnd: count, Before: before, After: after},
			{Path: "first.rs", Start: 513, End: 514, CurrentStart: 513, CurrentEnd: 514, Before: "before", After: "after"},
		},
		"second.rs": {{Path: "second.rs", Start: 0, End: 0, CurrentStart: 0, CurrentEnd: 1, After: "next\n"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("parsed bytes or spans changed")
	}
}
