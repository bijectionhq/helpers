package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"concave/internal/artifact"
	"concave/internal/repository"
)

// IsReference recognizes only content-addressed records, never arbitrary JSON
// source files such as concave.json.
func IsReference(ref string) bool {
	name := strings.TrimSuffix(filepath.Base(strings.TrimPrefix(ref, "@")), ".json")
	if len(name) < 10 || len(name) > 66 || !strings.Contains("rcv", name[:1]) || name[1] != '-' {
		return false
	}
	return strings.Trim(name[2:], "0123456789abcdef") == ""
}

// Store publishes immutable bytes before admitting them to their contribution.
// A crash before the membership link leaves only an unacknowledged object; retry
// completes admission. Neither lookup nor publication scans unrelated records.
func Store(repo *repository.Repo, r *Contribution) (string, error) {
	if err := r.Seal(); err != nil {
		return "", err
	}
	if _, err := repo.Git(nil, "check-ignore", "-q", "--", "target/concave/reviews/review.json"); err != nil {
		return "", fmt.Errorf("review store must be ignored: %w", err)
	}
	dir := objectDirectory(repo, r.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path, err := Write(repo, r, dir)
	if err != nil {
		return "", err
	}
	origin := r.ID
	if r.Origin != "" {
		origin = r.Origin
	}
	if len(origin) != 66 || !IsReference(origin) {
		return "", fmt.Errorf("invalid contribution origin")
	}
	members := contributionDirectory(repo, origin)
	if err := os.MkdirAll(members, 0700); err != nil {
		return "", err
	}
	member := filepath.Join(members, r.ID+".json")
	if err := os.Link(path, member); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		existing, readErr := Read(repo, member)
		if readErr != nil || existing.ID != r.ID {
			return "", fmt.Errorf("invalid contribution member: %v", readErr)
		}
	}
	return path, nil
}

// StoredPath validates an existing record without creating or admitting one.
func StoredPath(repo *repository.Repo, r *Contribution) (string, error) {
	if len(r.ID) != 66 || !IsReference(r.ID) {
		return "", fmt.Errorf("invalid revision ID")
	}
	path := filepath.Join(objectDirectory(repo, r.ID), r.ID+".json")
	stored, err := Read(repo, path)
	if err != nil {
		return "", err
	}
	digest, err := r.digest()
	if err != nil || stored.ID != r.prefix()+digest {
		return "", fmt.Errorf("stored review differs from requested contribution")
	}
	return path, nil
}

func objectDirectory(repo *repository.Repo, id string) string {
	return filepath.Join(repo.Root, "target/concave/reviews/objects", id[2:10])
}

func contributionDirectory(repo *repository.Repo, origin string) string {
	return filepath.Join(repo.Root, "target/concave/reviews/contributions", origin[2:10], origin)
}

// Write retains an immutable revision in a directory admitted by OutputDirectory,
// or in the ignored store. Candidate-check artifacts must survive its checkout.
func Write(repo *repository.Repo, r *Contribution, dir string) (string, error) {
	if err := r.Seal(); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if len(data) > repository.MaxOutput {
		return "", fmt.Errorf("review exceeds 128 MiB")
	}
	path := filepath.Join(dir, r.ID+".json")
	// Publish a complete immutable file atomically. A reader or competing writer
	// must never observe a partially written review after interruption.
	f, err := os.CreateTemp(dir, ".review-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		old, readErr := Read(repo, path)
		if readErr != nil || old.ID != r.ID {
			return "", fmt.Errorf("existing review is invalid: %v", readErr)
		}
	}
	return path, nil
}

func Read(repo *repository.Repo, ref string) (*Contribution, error) {
	if strings.HasPrefix(ref, "@") {
		return resolve(repo, strings.TrimPrefix(ref, "@"))
	}
	path := ref
	if (strings.HasPrefix(ref, "r-") || strings.HasPrefix(ref, "c-") || strings.HasPrefix(ref, "v-")) && !strings.ContainsAny(ref, "/\\") {
		prefix := ref[2:]
		if len(prefix) < 8 || len(prefix) > 64 || strings.Trim(prefix, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("review ID needs 8..64 hexadecimal digits")
		}
		dir := objectDirectory(repo, ref)
		if len(prefix) == 64 {
			path = filepath.Join(dir, ref+".json")
		} else {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, fmt.Errorf("review lookup: %w; recapture with concave review <paths>", err)
			}
			matches := []string{}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasPrefix(entry.Name(), ref) && strings.HasSuffix(entry.Name(), ".json") {
					matches = append(matches, entry.Name())
				}
			}
			if len(matches) != 1 {
				return nil, fmt.Errorf("review prefix has %d matches; use its full ID", len(matches))
			}
			path = filepath.Join(dir, matches[0])
		}
	}
	// Check the envelope before decoding the current schema. Older formats can
	// have different fields and must request recapture, not report corruption.
	var envelope struct {
		Format int `json:"format"`
	}
	data, err := artifact.ReadBytes(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("review has no valid format: %w", err)
	}
	if envelope.Format != ContributionFormat {
		return nil, fmt.Errorf("unsupported review format %d (expected %d); recapture the source with concave review <paths> and reselect your hunks", envelope.Format, ContributionFormat)
	}
	r := new(Contribution)
	if err := artifact.Decode(data, r); err != nil {
		return nil, err
	}
	digest, err := r.digest()
	if err != nil {
		return nil, fmt.Errorf("compute revision ID: %w", err)
	}
	if r.ID != r.prefix()+digest {
		return nil, fmt.Errorf("review digest mismatch: artifact integrity check failed; recapture from source")
	}
	if r.Verification != nil && (r.Candidate == nil || r.Verification.Candidate == "") {
		return nil, fmt.Errorf("verification lacks candidate identity")
	}
	if c := r.Candidate; c != nil {
		if c.Commit == "" || c.Tree == "" || c.Parent == "" {
			return nil, fmt.Errorf("incomplete candidate record")
		}
		if err := c.Content.Validate(); err != nil {
			return nil, err
		}
	}
	return r, nil
}
