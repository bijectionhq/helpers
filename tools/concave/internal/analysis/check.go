package analysis

import (
	"fmt"
	"slices"

	"concave/internal/policy"
	"concave/internal/repository"
)

// CheckOptions selects a source enforcement operation, independent of presentation.
type CheckOptions struct {
	Rev     string
	Paths   []string
	Family  string
	Case    string
	Against string
	Changed bool
}

// Check owns scope expansion and reviewed-reference checks.
// Empty changed scopes use the same report and snapshot accounting as other checks.
func Check(repo *repository.Repo, config *policy.Config, options CheckOptions) (*Report, error) {
	session := policy.NewSession(repo, config)
	paths := options.Paths
	fullAudit := len(paths) == 0 && options.Family == "" && options.Case == "" && !options.Changed
	var comparisonPaths []string
	// The policy changed-scope discovery loaded, keyed by its commit.
	var loadedBase string
	var loadedPolicy *policy.Config
	if options.Changed {
		if len(paths) > 0 || options.Family != "" || options.Case != "" {
			return nil, fmt.Errorf("--changed owns scope; omit path, family and case selectors")
		}
		var err error
		paths, fullAudit, loadedBase, loadedPolicy, err = changedScope(repo, config, options.Against, options.Rev)
		if err != nil {
			return nil, err
		}
		// A non-nil empty comparison is an empty scope, never a repository audit.
		comparisonPaths = append([]string{}, paths...)
		// An expanded scope is the ordinary full audit, not a selection of every
		// path; the old and new inventory still find retired boundaries below.
		if fullAudit {
			paths = nil
		}
	}
	// A scoped check re-measures every document importing a selected partial
	// or snippet: their identities include its bytes.
	if !fullAudit && len(paths) > 0 {
		importers, err := documentImporters(repo, config, options.Rev, paths, session)
		if err != nil {
			return nil, err
		}
		paths = append(slices.Clone(paths), importers...)
		if options.Changed {
			comparisonPaths = append(comparisonPaths, importers...)
		}
	}
	analyzed := comparisonPaths
	if fullAudit {
		analyzed = nil
	}
	report, err := analyze(repo, config, Options{Rev: options.Rev, Paths: paths, ComparisonPaths: analyzed, Family: options.Family, Case: options.Case}, session)
	if err != nil {
		return nil, err
	}
	if options.Changed {
		if fullAudit {
			report.Coverage.Scope = "repository"
		} else {
			report.Coverage.Scope = "changed"
		}
	}
	reference := options.Against
	if reference == "" && options.Rev == "" {
		reference = report.Head
	}
	if reference != "" {
		referenceRevision, err := repo.Resolve(reference)
		if err != nil {
			return nil, err
		}
		target := options.Rev
		if target == "" {
			target = report.Head
		}
		if err = repo.Ancestor(referenceRevision, target); err != nil {
			return nil, err
		}
		tree, err := repo.Tree(referenceRevision)
		if err != nil {
			return nil, err
		}
		if _, ok := tree[policy.File]; ok {
			previous := loadedPolicy
			if referenceRevision != loadedBase {
				if previous, err = policy.Load(repo, referenceRevision); err != nil {
					return nil, err
				}
			}
			if !report.PolicyCurrent(previous, config) {
				report.Findings = append(report.Findings, policy.Finding{Kind: "policy_changed", Path: policy.File, Message: "policy or transformations differ from reviewed reference; review explicitly before adopting the new reference"})
			}
		} else if options.Against != "" {
			return nil, fmt.Errorf("reviewed reference has no Concave policy")
		}
		seen := map[string]bool{}
		for _, row := range report.Files {
			seen[row.Path] = true
			if row.Upstream != nil || !repository.Matches(row.Path, config.Boundaries) {
				continue
			}
			before := tree[row.Path]
			oid, mode := "", ""
			if row.Selected != nil {
				oid, mode = row.Selected.OID, row.Selected.Mode
			}
			if before.OID != oid || before.Mode != mode {
				report.Findings = append(report.Findings, boundaryChanged(row.Path))
			}
		}
		// Retired fork-owned files have no row in the upstream/target union.
		for _, path := range comparisonPaths {
			if !seen[path] && tree[path].OID != "" && repository.Matches(path, config.Boundaries) {
				report.Findings = append(report.Findings, boundaryChanged(path))
			}
		}
	}

	if options.Rev == "" {
		now, err := policy.Load(repo, "")
		if err != nil {
			return nil, err
		}
		if !report.PolicyCurrent(config, now) {
			return nil, &repository.Stale{Message: "input changed: " + policy.File}
		}
	}
	report.Status = "passed"
	if len(report.Findings) > 0 {
		report.Status = "failed"
	}
	policy.NormalizeFindings(report.Findings)
	next := report.CheckAction(repo.Root, config)
	report.Next = &next
	return report, nil
}

func boundaryChanged(path string) policy.Finding {
	return policy.Finding{Kind: "boundary_changed", Path: path, Message: "fork-owned execution boundary changed since reference"}
}
