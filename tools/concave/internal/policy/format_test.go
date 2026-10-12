package policy

import (
	"strings"
	"testing"
)

// A baseline written before a rename decodes only through its format's rename
// entries; anything they do not rename is still refused by the strict decode.
func TestEarlierPolicyFormatsDecodeOnlyThroughTheirRenames(t *testing.T) {
	earlier := []byte(`{"format": 1, "upstream_pin": "abc", "environments": {"docs": {"dir": "npm-packages", "packages": ["docs"], "timeout_seconds": 60}}}`)
	if _, err := decodePolicy(earlier); err == nil || !strings.Contains(err.Error(), "format 1 is not readable") {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(migrations, 1) })
	migrations[1] = []Rename{{From: "upstream_pin", To: "upstream"}, {In: "environments.*", From: "dir", To: "directory"}}
	config, err := decodePolicy(earlier)
	if err != nil || config.Format != Format || config.Upstream != "abc" || config.Environments["docs"].Directory != "npm-packages" || config.Environments["docs"].TimeoutSeconds != 60 {
		t.Fatal(config, err)
	}
	// "*" reaches every member of a list as well as a map.
	migrations[1] = append(migrations[1], Rename{In: "documents.correspondences.*", From: "file", To: "path"})
	listed, err := decodePolicy([]byte(`{"format": 1, "upstream_pin": "abc", "documents": {"paths": ["apps/docs/"], "correspondences": [{"file": "apps/docs/a.mdx", "upstream_path": "docs/a.mdx"}]}}`))
	if err != nil || listed.Documents.Correspondences[0].Path != "apps/docs/a.mdx" {
		t.Fatal(listed, err)
	}
	if _, err := decodePolicy([]byte(`{"format": 1, "upstream_pin": "abc", "retired": true}`)); err == nil {
		t.Fatal("a field no rename covers was accepted")
	}
	// An empty target removes a retired field.
	migrations[1] = append(migrations[1], Rename{From: "retired"})
	if _, err := decodePolicy([]byte(`{"format": 1, "upstream_pin": "abc", "retired": true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := decodePolicy([]byte(`{"format": 1, "upstream_pin": "abc", "upstream": "def"}`)); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatal(err)
	}
}
