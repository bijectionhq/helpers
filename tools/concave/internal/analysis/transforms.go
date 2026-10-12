package analysis

import (
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/syntax"
	"fmt"
)

// transformationHunks derives authorized source edits from the pinned original,
// never from candidate changes. Invalid recipes are diagnosed by classification.
func transformationHunks(r *repository.Repo, c *policy.Config, contents map[string]sourcePair, engine *syntax.Engine) (map[string][]policy.TransformPlan, error) {
	inputs := []syntax.RewriteInput{}
	for _, rule := range c.Transforms {
		if rule.ASTGrep == nil {
			continue
		}
		for _, path := range rule.Paths {
			d, ok := contents[path]
			if ok && repository.Text(d.a) && repository.Text(d.b) {
				inputs = append(inputs, syntax.RewriteInput{Path: path, Source: d.a, Recipe: *rule.ASTGrep})
			}
		}
	}
	if _, err := engine.RewriteBatch(inputs); err != nil {
		return nil, err
	}
	// Policy gives each path at most one transformation.
	pairs := map[string]repository.SourcePair{}
	owners := map[string]string{}
	total := 0
	for _, rule := range c.Transforms {
		for _, path := range rule.Paths {
			d, ok := contents[path]
			if !ok || !repository.Text(d.a) || !repository.Text(d.b) {
				continue
			}
			if rule.ASTGrep != nil {
				if err := engine.Check(); err != nil {
					return nil, err
				}
			}
			want, err := rule.Apply(path, c.Manifest, string(d.a), engine)
			if err != nil {
				if syntax.IsUnavailable(err) {
					return nil, err
				}
				continue
			}
			total += len(d.a) + len(want)
			if total > repository.MaxOutput {
				return nil, fmt.Errorf("transformation comparisons exceed memory bound")
			}
			pairs[path] = repository.SourcePair{Before: d.a, After: []byte(want)}
			owners[path] = rule.ID
		}
	}
	hunks, err := r.DiffSources(pairs)
	if err != nil {
		return nil, err
	}
	plans := map[string][]policy.TransformPlan{}
	for path, id := range owners {
		plans[path] = []policy.TransformPlan{{ID: id, Hunks: hunks[path]}}
	}
	return plans, nil
}
