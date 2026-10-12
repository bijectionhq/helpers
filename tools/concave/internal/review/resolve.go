package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/repository"
)

// Reference follows one explicit contribution. Immutable IDs remain exact. No
// global current review or mutable pointer can mix two developers' selections.
func (r *Contribution) Reference() string {
	if r.Origin != "" {
		return "@" + r.Origin
	}
	return "@" + r.ID
}

func resolve(repo *repository.Repo, ref string) (*Contribution, error) {
	root, err := Read(repo, ref)
	if err != nil {
		return nil, err
	}
	origin := root.ID
	if root.Origin != "" {
		origin = root.Origin
	}
	directory := contributionDirectory(repo, origin)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, fmt.Errorf("contribution lookup exceeds 10000 records; use an immutable ID")
	}
	records := map[string]*Contribution{}
	parents := map[string]bool{}
	var size int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !IsReference(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		size += info.Size()
		if size > 256<<20 {
			return nil, fmt.Errorf("contribution discovery exceeds 256 MiB; use an immutable ID")
		}
		// Membership is immutable and local to this review. Validate its
		// bytes and origin before using it to resolve concurrent revisions.
		r, err := Read(repo, filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("contribution discovery: %w", err)
		}
		if r.ID+".json" != entry.Name() || (r.ID != origin && r.Origin != origin) {
			return nil, fmt.Errorf("invalid contribution membership %s", entry.Name())
		}
		records[r.ID] = r
		parents[r.Parent] = true
		if r.Verification != nil {
			parents[r.Verification.Candidate] = true
		}
	}
	leaves := []string{}
	for id := range records {
		if !parents[id] {
			leaves = append(leaves, id)
		}
	}
	sort.Strings(leaves)
	if len(leaves) != 1 {
		return nil, fmt.Errorf("contribution @%s has %d concurrent revisions; choose an immutable ID: %s", origin, len(leaves), strings.Join(leaves, ", "))
	}
	return records[leaves[0]], nil
}
