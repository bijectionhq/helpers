package verification

import (
	"time"

	"concave/internal/policy"
)

// Budget reserves each shared preparation once, each selected execution, both
// tool admissions and bounded source analysis/cleanup. Timings are not evidence.
func Budget(config *policy.Config, names []string) time.Duration {
	budget := 2 * time.Minute
	environments := map[string]bool{}
	// A candidate checkout may prepare the document parser to evaluate source.
	if config.Documents != nil && config.Documents.Environment != "" {
		environments[config.Documents.Environment] = true
		budget += time.Duration(config.Environments[config.Documents.Environment].TimeoutSeconds) * time.Second
	}
	checks := map[string]bool{}
	for _, name := range names {
		if checks[name] {
			continue
		}
		checks[name] = true
		check := config.Checks[name]
		budget += time.Duration(check.TimeoutSeconds)*time.Second + time.Duration(len(check.Toolchain))*20*time.Second
		for _, environment := range config.EnvironmentClosure(check.Environment) {
			if !environments[environment] {
				environments[environment] = true
				budget += time.Duration(config.Environments[environment].TimeoutSeconds) * time.Second
			}
		}
	}
	return budget
}
