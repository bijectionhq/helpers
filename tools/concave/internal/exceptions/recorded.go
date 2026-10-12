package exceptions

import (
	"fmt"
	"reflect"

	"concave/internal/policy"
	"concave/internal/repository"
)

// ValidateRecorded checks selected approval identities and source classification
// after recording. Unrelated approvals and a newer HEAD do not redefine them.
func ValidateRecorded(repo *repository.Repo, config *policy.Config, p *Proposal) error {
	digest, err := p.proposalDigest()
	if err != nil || p.Format != ProposalFormat || p.Kind != "concave-exception-proposal" || digest != p.ProposalSHA256 || len(p.Changes) == 0 {
		return fmt.Errorf("invalid exception proposal")
	}
	ids := []string{}
	for _, change := range p.Changes {
		if change == nil {
			return fmt.Errorf("invalid empty approval change")
		}
		current := lookup(config, change.ID)
		if !reflect.DeepEqual(current, change.Next) {
			return &repository.Stale{Message: "recorded approval changed: " + change.ID}
		}
		if current != nil {
			ids = append(ids, current.ID)
		}
		for _, old := range change.Previous {
			if old.ID != change.ID && lookup(config, old.ID) != nil {
				return &repository.Stale{Message: "replaced approval remains: " + old.ID}
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	explanations, err := Explain(repo, config, ids, "")
	if err != nil {
		return err
	}
	for _, e := range explanations {
		if e.Status != "valid" {
			return &repository.Stale{Message: "recorded source no longer valid: " + e.ID}
		}
	}
	return nil
}
