package policy

import (
	"fmt"
	"strings"

	"concave/internal/document"
	"concave/internal/repository"
)

// Correspondence supplies an upstream identity; it grants no approval. Original
// paths remain in the inventory so an unused preserved tree cannot hide drift.
type Correspondence struct {
	Path         string `json:"path"`
	UpstreamPath string `json:"upstream_path"`
}
type Documents struct {
	UpstreamRoot    string           `json:"upstream_root,omitempty"`
	Paths           []string         `json:"paths"`
	Correspondences []Correspondence `json:"correspondences"`
	// Environment installs the document parser in a candidate checkout, which
	// has no dependencies until an environment is prepared.
	Environment string `json:"environment,omitempty"`
}

func (c *Config) validateDocuments() error {
	if c.Documents == nil {
		return nil
	}
	if c.Documents.UpstreamRoot != "" && (!repository.ValidPath(c.Documents.UpstreamRoot) || !strings.HasSuffix(c.Documents.UpstreamRoot, "/")) {
		return fmt.Errorf("document upstream root needs a directory selector")
	}
	if len(c.Documents.Paths) == 0 {
		return fmt.Errorf("documents need explicit publishing source paths")
	}
	if _, ok := c.Environments[c.Documents.Environment]; c.Documents.Environment != "" && !ok {
		return fmt.Errorf("documents name unknown parser environment %s", c.Documents.Environment)
	}
	for _, p := range c.Documents.Paths {
		if !repository.ValidPath(p) {
			return fmt.Errorf("unsafe document selector %q", p)
		}
	}
	seen := map[string]bool{}
	original := map[string]bool{}
	for _, m := range c.Documents.Correspondences {
		if !repository.ValidPath(m.Path) || !repository.ValidPath(m.UpstreamPath) || m.Path == m.UpstreamPath || !document.Supports(m.Path) || !document.Supports(m.UpstreamPath) || !repository.Matches(m.Path, c.Documents.Paths) || seen[m.Path] || original[m.UpstreamPath] {
			return fmt.Errorf("invalid or ambiguous document correspondence %q", m.Path)
		}
		seen[m.Path] = true
		original[m.UpstreamPath] = true
	}
	for _, m := range c.Documents.Correspondences {
		if seen[m.UpstreamPath] {
			return fmt.Errorf("chained document correspondence %q", m.Path)
		}
	}
	return nil
}
func (c *Config) UpstreamPath(path string) string {
	if c.Documents != nil {
		for _, m := range c.Documents.Correspondences {
			if m.Path == path {
				return m.UpstreamPath
			}
		}
	}
	return path
}
func (c *Config) UpstreamTree(r *repository.Repo) (map[string]repository.Entry, error) {
	tree, err := r.Tree(c.Upstream)
	if err != nil {
		return nil, err
	}
	if c.Documents != nil {
		for _, m := range c.Documents.Correspondences {
			entry, ok := tree[m.UpstreamPath]
			if !ok || entry.Kind != "blob" {
				return nil, fmt.Errorf("correspondence source absent: %s", m.UpstreamPath)
			}
			if _, ok = tree[m.Path]; ok {
				return nil, fmt.Errorf("correspondence shadows inherited path: %s", m.Path)
			}
			tree[m.Path] = entry
		}
	}
	return tree, nil
}
