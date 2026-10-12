package analysis

import (
	"concave/internal/policy"
	"concave/internal/repository"
)

// SourceFindings measures proposed bytes with the ordinary transformation,
// restoration and exception classifiers. It does not execute witnesses.
func SourceFindings(repo *repository.Repo, config *policy.Config, path string, before, after []byte) ([]policy.Finding, error) {
	session := policy.NewSession(repo, config)
	pairs := map[string]sourcePair{path: {a: before, b: after}}
	plans, err := transformationHunks(repo, config, pairs, session.Syntax)
	if err != nil {
		return nil, err
	}
	restored, err := restorationCandidates(repo, config, config.Upstream, pairs, nil)
	if err != nil {
		return nil, err
	}
	hunks, err := repo.DiffSources(map[string]repository.SourcePair{path: {Before: before, After: after}})
	if err != nil {
		return nil, err
	}
	_, _, findings := session.ClassifyRestorations(path, before, after, hunks[path], restored[path], plans[path])
	policy.NormalizeFindings(findings)
	return findings, nil
}
