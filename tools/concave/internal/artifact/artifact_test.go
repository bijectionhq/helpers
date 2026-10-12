package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRejectsAmbiguousOversizedAndNonregularArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	for _, data := range []string{`{"value":1,"value":2}`, `{"extra":1}`, `{"value":1} {"value":2}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var report struct {
			Value int `json:"value"`
		}
		if _, err := Read(path, &report); err == nil {
			t.Fatal("accepted ambiguous report", data)
		}
	}
	if err := os.Truncate(path, MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	var value any
	if _, err := Read(path, &value); err == nil || !strings.Contains(err.Error(), "exceeds 128 MiB") {
		t.Fatal(err)
	}
	if _, err := Read(filepath.Dir(path), &value); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"value":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, &value); err != nil {
		t.Fatal(err)
	}
}
