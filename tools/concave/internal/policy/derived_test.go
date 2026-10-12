package policy_test

import (
	"strings"
	"testing"

	"concave/internal/policy"
)

func derivedConfig() *policy.Config {
	return &policy.Config{
		Format:   2,
		Upstream: strings.Repeat("a", 40),
		Roots:    []string{"crates"},
		Families: []policy.Family{{ID: "engine", Title: "Engine", Paths: []string{"crates/"}}},
		Checks:   map[string]policy.CheckDefinition{"fresh": {Command: []string{"true"}, Directory: ".", Inputs: []string{"Cargo.lock", "crates/"}, Toolchain: []policy.ToolCheck{{Command: []string{"true"}}}, TimeoutSeconds: 1, Results: "command"}},
		Derived: []policy.Derived{{ID: "lockfile", Paths: []string{"Cargo.lock"}, Sources: []string{"Cargo.toml", "crates/"}, Generator: "cargo update --workspace --offline", Check: "fresh",
			Explanation: policy.Explanation{Why: "Resolution of the workspace manifests.", Witnesses: []string{"Cargo.toml"}}}},
	}
}

func TestDerivedDeclarationsAreValidated(t *testing.T) {
	if err := derivedConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	if c := derivedConfig(); c.DerivedFor("Cargo.lock") == nil || c.DerivedFor("Cargo.toml") != nil {
		t.Fatal("derived lookup")
	}
	for name, change := range map[string]func(*policy.Config){
		"no-paths":            func(c *policy.Config) { c.Derived[0].Paths = nil },
		"no-sources":          func(c *policy.Config) { c.Derived[0].Sources = nil },
		"unsafe-source":       func(c *policy.Config) { c.Derived[0].Sources = []string{"../crates/"} },
		"duplicate-source":    func(c *policy.Config) { c.Derived[0].Sources = []string{"crates/", "crates/"} },
		"no-generator":        func(c *policy.Config) { c.Derived[0].Generator = " " },
		"multiline-generator": func(c *policy.Config) { c.Derived[0].Generator = "one\ntwo" },
		"unknown-check":       func(c *policy.Config) { c.Derived[0].Check = "absent" },
		"empty-why":           func(c *policy.Config) { c.Derived[0].Explanation.Why = "" },
		"no-witness":          func(c *policy.Config) { c.Derived[0].Explanation.Witnesses = nil },
		"directory-path":      func(c *policy.Config) { c.Derived[0].Paths = []string{"crates/"} },
		"unbound-output":      func(c *policy.Config) { c.Derived[0].Paths = []string{"Cargo.toml"} },
		"duplicate-path": func(c *policy.Config) {
			c.Derived = append(c.Derived, c.Derived[0])
			c.Derived[1].ID = "second"
		},
		"duplicate-id": func(c *policy.Config) {
			c.Derived = append(c.Derived, c.Derived[0])
			c.Derived[1].Paths = []string{"crates/generated.rs"}
		},
		"exact": func(c *policy.Config) { c.PreserveExactly = []string{"Cargo.lock"} },
		"transform": func(c *policy.Config) {
			c.Transforms = []policy.Transform{{ID: "rename", Paths: []string{"Cargo.lock"}, Replacements: []policy.Replacement{{From: "a", To: "b"}}, Explanation: policy.Explanation{Why: "rename", Witnesses: []string{"Cargo.toml"}}}}
		},
		"exception": func(c *policy.Config) {
			c.Exceptions = []policy.Exception{{ID: "edit", Path: "Cargo.lock", UpstreamSHA256: strings.Repeat("a", 64), ReplacementSHA256: strings.Repeat("b", 64), Start: 0, End: 1, Reason: "edit", Witnesses: []string{"Cargo.toml"}}}
		},
		"restoration": func(c *policy.Config) {
			c.Restorations = []policy.Restoration{{ID: "history", RemovedBy: strings.Repeat("c", 40), Paths: []string{"Cargo.lock"}, Explanation: policy.Explanation{Why: "restore", Witnesses: []string{"Cargo.toml"}}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := derivedConfig()
			change(c)
			if err := c.Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestDerivedRuleNamesFreshnessOwner(t *testing.T) {
	for _, rule := range policy.Rules {
		if rule.ID == "derived/declared" && strings.Contains(rule.Help, "verify run") {
			return
		}
	}
	t.Fatal("derived/declared rule missing")
}
