package command

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

func derivedEntry(cfg *policy.Config, id string) *policy.Derived {
	for i := range cfg.Derived {
		if cfg.Derived[i].ID == id {
			return &cfg.Derived[i]
		}
	}
	return nil
}

func (a *app) registerRuleExplanation(parent *cobra.Command) {
	var rev string
	command := &cobra.Command{Use: "explain ID", Short: "Explain an enforcement rule, scoped transformation or derived entry without scanning source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		for _, rule := range policy.Rules {
			if rule.ID != args[0] {
				continue
			}
			if rev != "" {
				return fmt.Errorf("built-in rules belong to this executable; --rev selects transformation policy only")
			}
			if a.jsonMode {
				return output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-rule-explanation", "rule": rule})
			}
			_, err := fmt.Fprintf(a.out, "%s\n%s\n\n%s\n", rule.ID, rule.Description, rule.Help)
			return err
		}
		r, err := a.open()
		if err != nil {
			return err
		}
		defer r.Close()
		if rev != "" {
			rev, err = r.Resolve(rev)
			if err != nil {
				return err
			}
		}
		cfg, err := policy.Load(r, rev)
		if err != nil {
			return err
		}
		for _, rule := range cfg.Transforms {
			if rule.ID != args[0] {
				continue
			}
			if a.jsonMode {
				return output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-rule-explanation", "policy_sha256": cfg.SHA256, "rule": rule})
			}
			fmt.Fprintf(a.out, "%s\n%s\n", rule.ID, rule.Explanation.Why)
			for _, path := range rule.Paths {
				fmt.Fprintf(a.out, "  %s\n", path)
			}
			for _, witness := range rule.Explanation.Witnesses {
				fmt.Fprintf(a.out, "witness: %s\n", witness)
			}
			if rule.Explanation.RemoveWhen != "" {
				fmt.Fprintf(a.out, "remove when: %s\n", rule.Explanation.RemoveWhen)
			}
			for _, reference := range rule.Explanation.Preserves {
				fmt.Fprintf(a.out, "preserves: %s\n", reference)
			}
			if rule.ASTGrep != nil {
				recipe, err := json.MarshalIndent(rule.ASTGrep, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "ast-grep recipe:\n%s\n", recipe)
			}
			replacements := rule.Replacements
			if rule.Module != "" {
				replacements = append([]policy.Replacement{}, replacements...)
				for _, pair := range cfg.Manifest.Rewrites[rule.Module] {
					replacements = append(replacements, policy.Replacement{From: pair[0], To: pair[1]})
				}
			}
			for _, replacement := range replacements {
				fmt.Fprintf(a.out, "replace %q → %q; paths=%v\n", replacement.From, replacement.To, replacement.Paths)
			}
			if rule.Tokens != nil {
				for _, replacement := range rule.Tokens.Rules {
					fmt.Fprintf(a.out, "rename whole name %q → %q; paths=%v\n", replacement.From, replacement.To, replacement.Paths)
				}
				for _, except := range rule.Tokens.Except {
					fmt.Fprintf(a.out, "except %q\n", except)
				}
			}
			for _, fixture := range rule.Tests {
				if fixture.Before == fixture.After {
					fmt.Fprintf(a.out, "Counterexample %s (%s), must stay unchanged:\n%s\n", fixture.Name, fixture.Path, fixture.Before)
				} else {
					fmt.Fprintf(a.out, "Fixture %s:\n%s", fixture.Name, repository.Patch(fixture.Path, []repository.Hunk{{Start: 0, End: len(repository.Lines(fixture.Before)), CurrentStart: 0, CurrentEnd: len(repository.Lines(fixture.After)), Before: fixture.Before, After: fixture.After}}))
				}
			}
			fmt.Fprintf(a.out, "%d fixtures; explanation only, source and witnesses have not been verified.\n", len(rule.Tests))
			for _, next := range []struct{ label, operation string }{{"Inspect recipe", "inspect"}, {"Test fixtures", "test"}} {
				args := []string{"concave", "--repo", r.Root, "rule", next.operation, rule.ID}
				if rev != "" {
					args = append(args, "--rev", rev)
				}
				if _, err := fmt.Fprintf(a.out, "%s: %s\n", next.label, process.ShellCommand(args)); err != nil {
					return err
				}
			}
			return nil
		}
		if entry := derivedEntry(cfg, args[0]); entry != nil {
			if a.jsonMode {
				return output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-rule-explanation", "policy_sha256": cfg.SHA256, "derived": entry})
			}
			fmt.Fprintf(a.out, "%s (derived)\n%s\n", entry.ID, entry.Explanation.Why)
			for _, path := range entry.Paths {
				fmt.Fprintf(a.out, "  %s\n", path)
			}
			for _, source := range entry.Sources {
				fmt.Fprintf(a.out, "source: %s\n", source)
			}
			fmt.Fprintf(a.out, "generator: %s\nfreshness check: %s\n", entry.Generator, entry.Check)
			for _, witness := range entry.Explanation.Witnesses {
				fmt.Fprintf(a.out, "witness: %s\n", witness)
			}
			if entry.Explanation.RemoveWhen != "" {
				fmt.Fprintf(a.out, "remove when: %s\n", entry.Explanation.RemoveWhen)
			}
			fmt.Fprintln(a.out, "Explanation only; freshness and sources have not been verified.")
			_, err := fmt.Fprintf(a.out, "Check freshness: %s\n", process.ShellCommand([]string{"concave", "--repo", r.Root, "verify", "run", entry.Check, "--output", "<new-directory>"}))
			return err
		}
		return fmt.Errorf("unknown rule %q; use concave list for enforcement, transformation and derived IDs", args[0])
	}}
	command.Flags().StringVar(&rev, "rev", "", "commit to read policy from (default: working policy)")
	for _, rule := range policy.Rules {
		command.ValidArgs = append(command.ValidArgs, rule.ID)
	}
	parent.AddCommand(command)
}
