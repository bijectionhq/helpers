package command

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"concave/internal/contracts"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type ruleFile struct {
	Path           string            `json:"path"`
	UpstreamSHA256 string            `json:"upstream_sha256"`
	ExpectedSHA256 string            `json:"expected_sha256"`
	Matches        []syntax.Match    `json:"matches,omitempty"`
	Hunks          []repository.Hunk `json:"hunks"`
}
type ruleTest struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

func (a *app) registerRules(root *cobra.Command) {
	parent := &cobra.Command{Use: "rule", Short: "Explain enforcement rules; inspect and test scoped transformations"}
	for _, operation := range []string{"inspect", "test"} {
		var rev string
		c := &cobra.Command{Use: operation + " ID", Args: cobra.ExactArgs(1), Short: map[string]string{"inspect": "Show a scoped transformation's upstream matches and expected hunks", "test": "Run a scoped transformation's declared fixtures"}[operation], RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.open()
			if err != nil {
				return err
			}
			defer r.Close()
			snapshot, err := repository.OpenSnapshot(r, rev)
			if err != nil {
				return err
			}
			cfg, err := policy.Load(r, snapshot.Revision)
			if err != nil {
				return err
			}
			var rule *policy.Transform
			for i := range cfg.Transforms {
				if cfg.Transforms[i].ID == args[0] {
					rule = &cfg.Transforms[i]
					break
				}
			}
			if rule == nil {
				if entry := derivedEntry(cfg, args[0]); entry != nil {
					return fmt.Errorf("derived %s has no recipe; its freshness check is %s (concave verify run %s --output DIR)", entry.ID, entry.Check, entry.Check)
				}
				return fmt.Errorf("unknown transformation %q", args[0])
			}
			engine := syntax.New(r.Context, r.Root)
			if rule.ASTGrep != nil {
				if err = engine.Check(); err != nil {
					return err
				}
			}
			if operation == "test" {
				if len(rule.Tests) == 0 {
					return fmt.Errorf("transformation %s has no fixtures; zero tests is not a passing result", rule.ID)
				}
				var native *syntax.NativeTests
				if rule.ASTGrep != nil {
					inputs := []syntax.RewriteInput{}
					fixtures := []syntax.Fixture{}
					for _, tc := range rule.Tests {
						inputs = append(inputs, syntax.RewriteInput{Path: tc.Path, Source: []byte(tc.Before), Recipe: *rule.ASTGrep})
						fixtures = append(fixtures, syntax.Fixture{Before: tc.Before, After: tc.After})
					}
					result, err := engine.TestFixtures(rule.ID, *rule.ASTGrep, fixtures)
					if err != nil {
						return err
					}
					native = &result
					if _, err = engine.RewriteBatch(inputs); err != nil {
						return err
					}
				}
				results := []ruleTest{}
				failed := native != nil && !native.Passed
				for _, tc := range rule.Tests {
					var want string
					var err error
					if rule.ASTGrep != nil {
						b, _, e := engine.Rewrite(tc.Path, []byte(tc.Before), *rule.ASTGrep)
						want, err = string(b), e
					} else {
						want, err = rule.Apply(tc.Path, cfg.Manifest, tc.Before)
					}
					result := ruleTest{Name: tc.Name, Passed: err == nil && want == tc.After}
					if err != nil {
						if syntax.IsUnavailable(err) {
							return err
						}
						result.Error = err.Error()
					} else if !result.Passed {
						result.Error = "output differs from expected bytes"
					}
					failed = failed || !result.Passed
					results = append(results, result)
				}
				now, err := policy.Load(r, snapshot.Revision)
				if err != nil {
					return err
				}
				if now.SHA256 != cfg.SHA256 {
					return &repository.Stale{Message: "input changed: " + policy.File}
				}
				if a.jsonMode {
					err = output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-rule-tests", "rule": rule.ID, "policy_sha256": cfg.SHA256, "engine": engineIdentity(rule), "native_tests": native, "tests": results})
				} else {
					if native != nil {
						fmt.Fprintf(a.out, "native ast-grep: %d cases passed=%t %s\n", native.Cases, native.Passed, native.Error)
					}
					for _, result := range results {
						fmt.Fprintf(a.out, "%s passed=%t %s\n", result.Name, result.Passed, result.Error)
					}
				}
				if err != nil {
					return err
				}
				if failed {
					return &Exit{Code: 1}
				}
				return nil
			}
			refs := contracts.New(snapshot.Read, engine)
			for _, path := range rule.Paths {
				if err = policy.CheckReferences(path, rule.Explanation, refs); err != nil {
					return err
				}
			}
			base, err := r.Resolve(cfg.Upstream)
			if err != nil {
				return err
			}
			target := snapshot.Revision
			if target == "" {
				target = snapshot.Head
			}
			if err = r.Ancestor(base, target); err != nil {
				return err
			}
			tree, err := cfg.UpstreamTree(r)
			if err != nil {
				return err
			}
			upstreamSources := map[string][]byte{}
			inputs := []syntax.RewriteInput{}
			inputBytes := 0
			for _, path := range rule.Paths {
				entry, ok := tree[path]
				if !ok {
					return fmt.Errorf("upstream source absent: %s", path)
				}
				before, err := r.Blob(entry.OID)
				if err != nil {
					return err
				}
				if !repository.Text(before) {
					return fmt.Errorf("upstream source is not bounded text: %s", path)
				}
				inputBytes += len(before)
				if inputBytes > repository.MaxOutput {
					return fmt.Errorf("recipe inspection exceeds memory bound")
				}
				upstreamSources[path] = before
				if rule.ASTGrep != nil {
					inputs = append(inputs, syntax.RewriteInput{Path: path, Source: before, Recipe: *rule.ASTGrep})
				}
			}
			if _, err = engine.RewriteBatch(inputs); err != nil {
				return err
			}
			rows := []ruleFile{}
			pairs := map[string]repository.SourcePair{}
			total := 0
			for _, path := range rule.Paths {
				before := upstreamSources[path]
				var want string
				var matches []syntax.Match
				if rule.ASTGrep != nil {
					b, m, e := engine.Transform(path, before, *rule.ASTGrep)
					want, matches, err = string(b), m, e
				} else {
					want, err = rule.Apply(path, cfg.Manifest, string(before))
				}
				if err != nil {
					return err
				}
				total += len(before) + len(want)
				if total > repository.MaxOutput {
					return fmt.Errorf("recipe inspection exceeds memory bound")
				}
				rows = append(rows, ruleFile{Path: path, UpstreamSHA256: repository.Digest(before), ExpectedSHA256: repository.Digest([]byte(want)), Matches: matches})
				pairs[path] = repository.SourcePair{Before: before, After: []byte(want)}
			}
			hunks, err := r.DiffSources(pairs)
			if err != nil {
				return err
			}
			for i := range rows {
				rows[i].Hunks = hunks[rows[i].Path]
			}
			digest, err := snapshot.Verify()
			if err != nil {
				return err
			}
			now, err := policy.Load(r, snapshot.Revision)
			if err != nil {
				return err
			}
			if now.SHA256 != cfg.SHA256 {
				return &repository.Stale{Message: "input changed: " + policy.File}
			}
			if a.jsonMode {
				return output.JSON(a.out, map[string]any{"format": 1, "kind": "concave-rule-inspection", "rule": rule, "upstream": base, "policy_sha256": cfg.SHA256, "snapshot_sha256": inspectionIdentity(rule, cfg.SHA256, base, digest, rows), "engine": engineIdentity(rule), "files": rows})
			}
			fmt.Fprintf(a.out, "%s · upstream %s\n", rule.ID, base)
			for _, row := range rows {
				fmt.Fprintf(a.out, "%s: %d exact edits\n", row.Path, len(row.Hunks))
			}
			return nil
		}}
		c.Flags().StringVar(&rev, "rev", "", "commit to read policy from (default: working policy)")
		parent.AddCommand(c)
	}
	a.registerRuleExplanation(parent)
	root.AddCommand(parent)
}

func engineIdentity(rule *policy.Transform) string {
	if rule.ASTGrep != nil {
		return syntax.Identity
	}
	return ""
}
func inspectionIdentity(rule *policy.Transform, policySHA, upstream, references string, files []ruleFile) string {
	b, _ := json.Marshal([]any{engineIdentity(rule), policySHA, upstream, references, rule, files})
	return repository.Digest(b)
}
