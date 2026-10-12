package verification

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/policy"
)

func checkResults(check policy.CheckDefinition, data []byte, roots ...string) (Results, error) {
	r, err := parseResults(check.Results, data)
	if (check.Results == "vitest-json" || check.Results == "jest-json") && len(roots) > 0 && roots[0] != "" {
		prefix := filepath.ToSlash(roots[0]) + "/"
		for i := range r.Cases {
			r.Cases[i].ID = strings.TrimPrefix(filepath.ToSlash(r.Cases[i].ID), prefix)
		}
	}
	if check.Results == "rust-test-json" {
		for i := range r.Cases {
			r.Cases[i].ID = check.Target + "::" + r.Cases[i].ID
		}
	}
	if err != nil {
		return r, err
	}
	return r, ValidateWitnesses(check, r)
}

func ValidateWitnesses(check policy.CheckDefinition, results Results) error {
	passed := map[string]bool{}
	for _, c := range results.Cases {
		passed[c.ID] = c.Status == "passed"
	}
	witnesses := make([]string, 0, len(check.Witnesses))
	for witness := range check.Witnesses {
		witnesses = append(witnesses, witness)
	}
	sort.Strings(witnesses)
	for _, witness := range witnesses {
		for _, id := range check.Witnesses[witness] {
			if !passed[id] {
				if check.Results == "go-test-json" {
					children := []string{}
					for _, result := range results.Cases {
						if strings.HasPrefix(result.ID, id+"/") {
							children = append(children, result.ID)
						}
					}
					if len(children) > 0 {
						sort.Strings(children)
						detail := strings.Join(children[:min(8, len(children))], ", ")
						if len(children) > 8 {
							detail += fmt.Sprintf("; %d more in the receipt", len(children)-8)
						}
						return fmt.Errorf("witness %s names parent test %q; Go counts leaf cases: map the exact subtest IDs: %s", witness, id, detail)
					}
				}
				return fmt.Errorf("witness %s requires a passed test %q; absent, failed or skipped", witness, id)
			}
		}
	}
	return nil
}
