package verification

import (
	"testing"
	"time"

	"concave/internal/policy"
)

func TestBudgetIncludesSharedPreparationOnce(t *testing.T) {
	config := &policy.Config{Environments: map[string]policy.Environment{"sdk": {TimeoutSeconds: 600}}, Checks: map[string]policy.CheckDefinition{
		"one": {Environment: "sdk", TimeoutSeconds: 1800},
		"two": {Environment: "sdk", TimeoutSeconds: 1800},
	}}
	if got := Budget(config, []string{"one", "two", "one"}); got != 72*time.Minute {
		t.Fatalf("budget = %s; want two runs plus one preparation and overhead", got)
	}
}

// An included environment's preparation counts once, like a shared one.
func TestBudgetCountsIncludedPreparationOnce(t *testing.T) {
	config := &policy.Config{Environments: map[string]policy.Environment{"fixtures": {TimeoutSeconds: 180, Includes: []string{"docs"}}, "docs": {TimeoutSeconds: 600}}, Checks: map[string]policy.CheckDefinition{
		"suite": {Environment: "fixtures", TimeoutSeconds: 600},
		"site":  {Environment: "docs", TimeoutSeconds: 600},
	}}
	if got := Budget(config, []string{"suite", "site"}); got != 2*600*time.Second+180*time.Second+600*time.Second+Budget(&policy.Config{}, nil) {
		t.Fatalf("budget = %s", got)
	}
}

// The document parser environment is reserved once, even when a check also
// prepares it.
func TestBudgetReservesTheDocumentParserOnce(t *testing.T) {
	config := &policy.Config{Documents: &policy.Documents{Environment: "docs"}, Environments: map[string]policy.Environment{"docs": {TimeoutSeconds: 600}}, Checks: map[string]policy.CheckDefinition{
		"site": {Environment: "docs", TimeoutSeconds: 60},
	}}
	if got := Budget(config, []string{"site"}); got != 2*time.Minute+600*time.Second+60*time.Second {
		t.Fatalf("budget = %s", got)
	}
}
