package exceptions

import (
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
	"fmt"
	"strings"
)

const historyLimit = 64

type PreviousSource struct {
	Status       string `json:"status"`
	Commit       string `json:"commit,omitempty"`
	Blob         string `json:"blob,omitempty"`
	SHA256       string `json:"sha256"`
	Examined     int    `json:"examined"`
	Limit        int    `json:"limit"`
	CurrentFence bool   `json:"current_fence"`
	Diff         string `json:"approved_to_current_diff,omitempty"`
}

// The policy stores a digest, not old bytes. One operation searches each source
// history once for all selected bindings, retaining only matching bodies.
type historicalBody struct {
	evidence PreviousSource
	body     string
}
type sourceHistory struct {
	session   *policy.Session
	repo      *repository.Repo
	head      string
	wanted    map[string][]policy.Exception
	recovered map[string]historicalBody
	retained  int
}

func newSourceHistory(repo *repository.Repo, head string, selected []policy.Exception) *sourceHistory {
	session := policy.NewSession(repo, &policy.Config{})
	history := &sourceHistory{session: session, repo: repo, head: head, wanted: map[string][]policy.Exception{}, recovered: map[string]historicalBody{}}
	for _, binding := range selected {
		history.wanted[binding.Path] = append(history.wanted[binding.Path], binding)
	}
	return history
}
func (history *sourceHistory) previous(binding policy.Exception, current []byte) (*PreviousSource, error) {
	if _, seen := history.recovered[binding.ID]; !seen {
		if err := history.load(binding.Path); err != nil {
			return nil, err
		}
	}
	recovered := history.recovered[binding.ID]
	evidence := recovered.evidence
	annotations, _ := history.session.Annotations(binding.Path, string(current))
	now, exists := annotations[binding.ID]
	evidence.CurrentFence = exists
	if evidence.Status == "recovered" && recovered.body != now.Code {
		evidence.Diff = string(recovery.SinglePatch(binding.Path, []byte(recovered.body), []byte(now.Code)))
	}
	return &evidence, nil
}
func (history *sourceHistory) load(path string) error {
	pending := map[string]policy.Exception{}
	for _, binding := range history.wanted[path] {
		evidence := PreviousSource{Status: "unavailable", SHA256: binding.ReplacementSHA256, Limit: historyLimit}
		switch {
		case !binding.Fence || binding.File != nil:
			evidence.Status = "not_a_source_fence"
		case strings.ContainsAny(path, "\r\n"):
			evidence.Status = "unsupported_history_path"
		default:
			pending[binding.ID] = binding
		}
		history.recovered[binding.ID] = historicalBody{evidence: evidence}
	}
	if len(pending) == 0 {
		return nil
	}
	commits, err := history.repo.Git(nil, "log", "-64", "--format=%H", history.head, "--", path)
	if err != nil {
		return err
	}
	refs := strings.Fields(string(commits))
	if len(refs) == 0 {
		return nil
	}
	var queries strings.Builder
	for _, ref := range refs {
		queries.WriteString(ref + ":" + path + "\n")
	}
	objects, err := history.repo.Git([]byte(queries.String()), "cat-file", "--batch-check")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	total := 0
	for index, line := range strings.Split(strings.TrimSpace(string(objects)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 || parts[1] != "blob" || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		data, err := history.repo.Blob(parts[0])
		if err != nil {
			return err
		}
		total += len(data)
		annotations, invalid := history.session.Annotations(path, string(data))
		for id, binding := range pending {
			recovered := history.recovered[id]
			recovered.evidence.Examined++
			old, exists := annotations[id]
			if total > 16<<20 {
				recovered.evidence.Status = "search_byte_limit"
			} else if repository.Text(data) && len(invalid) == 0 && exists && old.CodeSHA256() == binding.ReplacementSHA256 {
				history.retained += len(old.Code)
				if history.retained > repository.MaxOutput {
					return fmt.Errorf("approved source history exceeds retained byte bound")
				}
				recovered.evidence.Status = "recovered"
				recovered.evidence.Commit = refs[index]
				recovered.evidence.Blob = parts[0]
				recovered.body = old.Code
				delete(pending, id)
			}
			history.recovered[id] = recovered
		}
		if total > 16<<20 || len(pending) == 0 {
			break
		}
	}
	return nil
}
