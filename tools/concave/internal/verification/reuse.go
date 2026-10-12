package verification

import (
	"concave/internal/policy"
	"concave/internal/repository"
	"errors"
)

// ReuseDecision never treats missing or damaged evidence as a successful run.
// Reuse is opt-in: the check owner must describe its complete dependency boundary.
type ReuseDecision struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func Reuse(repo *repository.Repo, config *policy.Config, name, directory string, receipt *Receipt) ReuseDecision {
	if receipt == nil {
		return ReuseDecision{"missing", "no previous receipt"}
	}
	digest, err := receipt.digest()
	if err != nil || receipt.Format != ReceiptFormat || receipt.Kind != "concave-receipt" || digest != receipt.ReceiptSHA256 || receipt.Check != name {
		return ReuseDecision{"invalid", "receipt integrity or identity mismatch"}
	}
	if receipt.Status == Failed {
		return ReuseDecision{"failed", "previous execution failed"}
	}
	if err := ValidateLogs(directory, receipt); err != nil {
		return ReuseDecision{"invalid", err.Error()}
	}
	if err := Validate(repo, config, receipt); err != nil {
		var stale *repository.Stale
		if errors.As(err, &stale) {
			return ReuseDecision{"stale", err.Error()}
		}
		return ReuseDecision{"invalid", err.Error()}
	}
	if config.Checks[name].ReuseReason == "" {
		return ReuseDecision{"rerun", "check has no declared reusable dependency boundary"}
	}
	return ReuseDecision{"reusable", "definition, inputs, tools, cases and log integrity match"}
}
