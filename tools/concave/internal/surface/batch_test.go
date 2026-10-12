package surface_test

import (
	"concave/internal/surface"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCallerBatchMatchesIndependentCapturesAndReusesExactInputs(t *testing.T) {
	f := fixture(t)
	f.Write("other.ts", "export const other = 1;\n")
	f.Write("barrel.ts", "export * from './entry.js'; export * from './other.js';\n")
	f.Write("use.ts", "import { renamed, other } from './barrel.js'; renamed('x'); void other;\nimport { renamed as alias } from 'first'; alias('x');\nimport { other as second } from 'second'; void second;\n")
	repo, _ := f.Open()
	options := surface.Options{Mode: surface.Callers, Entries: []string{"other.ts", "entry.ts"}, Modules: map[string]string{"first": "entry.ts", "second": "other.ts"}}
	batch, err := surface.Capture(repo, options)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Coverage.Complete {
		t.Fatal(batch.Coverage)
	}
	for module, entry := range options.Modules {
		single, err := surface.Capture(repo, surface.Options{Mode: surface.Callers, Entries: []string{entry}, Modules: map[string]string{module: entry}})
		if err != nil {
			t.Fatal(err)
		}
		matched := []surface.Consumer{}
		for _, consumer := range batch.Consumers {
			if consumer.Entry == entry {
				matched = append(matched, consumer)
			}
		}
		if !reflect.DeepEqual(matched, single.Consumers) {
			t.Fatalf("batch differs for %s: %+v / %+v", entry, matched, single.Consumers)
		}
		if batch.Coverage.ScannedFiles != single.Coverage.ScannedFiles {
			t.Fatal("batch rescanned inventory", batch.Coverage, single.Coverage)
		}
	}
	oldHead, oldDigest := batch.Head, batch.SnapshotSHA256
	f.Write("notes.md", "unrelated documentation\n")
	f.Commit()
	if err = surface.Validate(repo, batch); err != nil {
		t.Fatal("unrelated commit invalidated source evidence", err)
	}
	if batch.Head != oldHead || batch.SnapshotSHA256 != oldDigest {
		t.Fatal("validation rewrote capture provenance")
	}
	f.Write("new.ts", "export const newConsumer=1;\n")
	if err = surface.Validate(repo, batch); err == nil {
		t.Fatal("new source inventory accepted")
	}
	if err = os.Remove(filepath.Join(f.Root, "new.ts")); err != nil {
		t.Fatal(err)
	}
	if err = surface.Validate(repo, batch); err != nil {
		t.Fatal(err)
	}
	f.Write("other.ts", "export const other=2;\n")
	if err = surface.Validate(repo, batch); err == nil {
		t.Fatal("changed input accepted")
	}
	for _, invalid := range []surface.Options{
		{Mode: surface.Callers, Entries: []string{"entry.ts", "entry.ts"}},
		{Mode: surface.Callers, Entries: []string{"entry.ts"}, Modules: map[string]string{"first": "other.ts"}},
	} {
		if _, err = surface.Capture(repo, invalid); err == nil {
			t.Fatal("invalid entry mapping accepted", invalid)
		}
	}
}
