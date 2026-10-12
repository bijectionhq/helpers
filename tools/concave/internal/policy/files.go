package policy

import (
	"bytes"
	"encoding/json"
	"fmt"

	"concave/internal/artifact"
	"concave/internal/repository"
)

// FileBinding is reserved for bounded binary replacements and file retirement.
// Text that remains in the tree always uses a bounded source span instead.
type FileBinding struct {
	Upstream    FileIdentity  `json:"upstream"`
	Replacement *FileIdentity `json:"replacement"`
}
type FileIdentity struct {
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

// Keep span coordinates explicit, while a file binding has no fictitious span.
func (x Exception) MarshalJSON() ([]byte, error) {
	if x.File == nil {
		type plain Exception
		return json.Marshal(plain(x))
	}
	return json.Marshal(struct {
		ID         string       `json:"id"`
		Path       string       `json:"path"`
		File       *FileBinding `json:"file"`
		Reason     string       `json:"reason"`
		Witnesses  []string     `json:"witnesses"`
		RemoveWhen string       `json:"remove_when,omitempty"`
		Preserves  []string     `json:"preserves,omitempty"`
	}{x.ID, x.Path, x.File, x.Reason, x.Witnesses, x.RemoveWhen, x.Preserves})
}

func (b *FileBinding) UnmarshalJSON(data []byte) error {
	var raw struct {
		Upstream    FileIdentity    `json:"upstream"`
		Replacement json.RawMessage `json:"replacement"`
	}
	if err := artifact.Decode(data, &raw); err != nil {
		return err
	}
	if len(raw.Replacement) == 0 {
		return fmt.Errorf("file binding requires replacement; use explicit null for retirement")
	}
	b.Upstream, b.Replacement = raw.Upstream, nil
	if !bytes.Equal(bytes.TrimSpace(raw.Replacement), []byte("null")) {
		b.Replacement = &FileIdentity{}
		return artifact.Decode(raw.Replacement, b.Replacement)
	}
	return nil
}
func (b FileBinding) Validate() error {
	valid := func(s FileIdentity) bool {
		return shaPattern.MatchString(s.SHA256) && (s.Mode == "100644" || s.Mode == "100755")
	}
	if !valid(b.Upstream) {
		return fmt.Errorf("upstream requires a regular file mode and SHA-256")
	}
	if b.Replacement != nil && (!valid(*b.Replacement) || b.Replacement.Mode != b.Upstream.Mode || *b.Replacement == b.Upstream) {
		return fmt.Errorf("replacement requires different bytes at the unchanged regular file mode")
	}
	return nil
}
func FileState(s *repository.State) *FileIdentity {
	if s == nil {
		return nil
	}
	return &FileIdentity{SHA256: s.SHA256, Mode: s.Mode}
}
func (s FileIdentity) Matches(actual *repository.State) bool {
	return actual != nil && actual.Kind == "file" && s.SHA256 == actual.SHA256 && s.Mode == actual.Mode
}
func FileEligible(before, after *repository.State, a, b []byte) error {
	if before == nil || before.Kind != "file" || a == nil {
		return fmt.Errorf("file binding requires bounded regular upstream bytes")
	}
	if after == nil {
		return nil
	}
	if after.Kind != "file" || before.Mode != after.Mode || b == nil {
		return fmt.Errorf("file replacement requires bounded regular bytes and unchanged mode")
	}
	if repository.Text(a) || repository.Text(b) {
		return fmt.Errorf("existing text requires a source-span exception, not a file binding")
	}
	return nil
}

// concave:invariant lookup-isolation
// Return a shallow copy of the matching exception; top-level edits by the caller
// must not change policy. Scanning nonmatching entries must allocate nothing.
func (c *Config) FileException(path string) *Exception {
	for i := range c.Exceptions {
		if c.Exceptions[i].Path == path && c.Exceptions[i].File != nil {
			// Copy only a matching entry. Taking the range variable address
			// allocates one full Exception for every entry on every lookup.
			x := c.Exceptions[i]
			return &x
		}
	}
	return nil
}
func (x Exception) ClassifyFile(before, after *repository.State, a, b []byte) (string, []string, []Finding) {
	fail := func(kind, message string) (string, []string, []Finding) {
		return "unexplained", []string{}, []Finding{{Kind: kind, Path: x.Path, Exception: x.ID, Rule: "exception/valid", Message: message}}
	}
	if !x.File.Upstream.Matches(before) {
		return fail("stale_exception", "upstream file identity changed")
	}
	if x.File.Upstream.Matches(after) {
		return fail("obsolete_exception", "original file restored; refresh to remove its binding")
	}
	if err := FileEligible(before, after, a, b); err != nil {
		return fail("stale_exception", err.Error())
	}
	if x.File.Replacement == nil {
		if after != nil {
			return fail("stale_exception", "retired path is present")
		}
	} else if !x.File.Replacement.Matches(after) {
		return fail("stale_exception", "replacement file identity changed")
	}
	return "excepted", []string{x.ID}, nil
}
