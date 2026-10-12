package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/repository"
)

// ValidateApplied proves the saved patch relates its recorded preimages to the
// current raw postimages. HEAD is provenance here, not an applicability guard.
// Reverse application happens only in private scratch, never in the checkout.
func ValidateApplied(r *repository.Repo, c *policy.Config, p *Plan) error {
	if p.Format != 1 || p.Kind != "concave-recovery-plan" || p.Reason == "" || len(p.Files) == 0 || len(p.Patch) > repository.MaxBytes {
		return fmt.Errorf("unsupported recovery plan")
	}
	if p.DocumentEngine != "" && p.DocumentEngine != document.Identity() {
		return stale("MDX engine changed")
	}
	if err := p.DocumentInputs.Verify(r, repository.SourceInputs); err != nil {
		return err
	}
	if c.Upstream != p.Upstream || recoveryEngine(c, p.Files) != p.SyntaxEngine {
		return stale("recovery basis changed; review again")
	}
	if repository.Digest([]byte(p.Patch)) != p.PatchSHA256 {
		return fmt.Errorf("patch digest mismatch")
	}
	scratch, err := os.MkdirTemp("", "concave-applied-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	private := &repository.Repo{Root: scratch, Context: r.Context}
	if _, err := private.Git(nil, "init", "--quiet"); err != nil {
		return err
	}
	paths := make([]string, 0, len(p.Files))
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		effect := p.Files[path]
		deleted := slices.Contains(p.DeletedPaths, path)
		if !repository.ValidPath(path) || deleted != (effect.After == nil) || effect.After != nil && effect.After.Kind != "file" {
			return fmt.Errorf("invalid recovery effect: %s", path)
		}
		state, data, err := r.Snapshot(path)
		if err != nil {
			return err
		}
		if !repository.Same(state, effect.After) {
			return stale("postimage changed: " + path)
		}
		if deleted {
			continue
		}
		dst := filepath.Join(scratch, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if state.Mode == "100755" {
			mode = 0700
		}
		if err := os.WriteFile(dst, data, mode); err != nil {
			return err
		}
	}
	if _, err := private.Git([]byte(p.Patch), "apply", "--reverse", "--unidiff-zero", "--whitespace=nowarn", "-"); err != nil {
		return fmt.Errorf("invalid applied patch: %w", err)
	}
	for _, path := range paths {
		state, data, err := private.Snapshot(path)
		if err != nil {
			return err
		}
		if state != nil {
			state.OID = r.OID(data)
		}
		if !repository.Same(state, p.Files[path].Before) {
			return fmt.Errorf("patch preimage differs from plan: %s", path)
		}
	}
	// Recompute the complete forward effect set, catching added or omitted paths.
	actual, err := CheckedPatch(private, []byte(p.Patch), p.CreatedPaths, p.DeletedPaths)
	if err != nil {
		return err
	}
	if len(actual) != len(p.Files) {
		return fmt.Errorf("patch paths differ from plan")
	}
	for path, effect := range actual {
		want, ok := p.Files[path]
		if !ok || !sameBytes(effect.Before, want.Before) || !sameBytes(effect.After, want.After) {
			return fmt.Errorf("patch effects differ from plan: %s", path)
		}
	}
	return nil
}

func sameBytes(a, b *repository.State) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Kind == b.Kind && a.Mode == b.Mode && a.SHA256 == b.SHA256
}
