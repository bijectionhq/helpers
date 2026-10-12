package syntax

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"concave/internal/repository"
)

type Fixture struct{ Before, After string }
type NativeTests struct {
	Cases  int    `json:"cases"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

// TestFixtures derives native valid/invalid cases from the single Concave policy.
// Native tests verify match intent; RewriteBatch verifies exact replacement bytes.
// No snapshots are generated or accepted automatically.
func (e *Engine) TestFixtures(id string, recipe Recipe, fixtures []Fixture) (NativeTests, error) {
	result := NativeTests{Cases: len(fixtures)}
	if len(fixtures) == 0 {
		return result, fmt.Errorf("native rule test requires fixtures")
	}
	if err := e.Check(); err != nil {
		return result, err
	}
	valid, invalid := []string{}, []string{}
	total := 0
	for _, tc := range fixtures {
		if err := sourceAllowed([]byte(tc.Before)); err != nil {
			return result, err
		}
		total += len(tc.Before) + len(tc.After)
		if total > repository.MaxOutput {
			return result, fmt.Errorf("native fixtures exceed 128 MiB")
		}
		if tc.Before == tc.After {
			valid = append(valid, tc.Before)
		} else {
			invalid = append(invalid, tc.Before)
		}
	}
	rule, err := recipeRule(id, recipe)
	if err != nil {
		return result, err
	}
	if _, err = encodeRules([]map[string]any{rule}); err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp("", "concave-rule-tests-")
	if err != nil {
		return result, &Unavailable{err}
	}
	defer os.RemoveAll(dir)
	for _, child := range []string{"rules", "tests"} {
		if err = os.Mkdir(filepath.Join(dir, child), 0700); err != nil {
			return result, &Unavailable{err}
		}
	}
	files := map[string]any{
		"sgconfig.yml":       map[string]any{"ruleDirs": []string{"rules"}, "testConfigs": []map[string]string{{"testDir": "tests"}}},
		"rules/rule.yml":     rule,
		"tests/fixtures.yml": map[string]any{"id": id, "valid": valid, "invalid": invalid},
	}
	for name, value := range files {
		data, err := json.Marshal(value)
		if err != nil {
			return result, err
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return result, &Unavailable{err}
		}
	}
	output, err := e.runAt(dir, nil, "test", "--config", filepath.Join(dir, "sgconfig.yml"), "--skip-snapshot-tests", "--color", "never")
	if err != nil {
		if IsUnavailable(err) {
			return result, err
		}
		result.Error = strings.TrimSpace(string(output) + "\n" + err.Error())
		return result, nil
	}
	if !strings.Contains(string(output), "test result: ok. 1 passed; 0 failed;") {
		return result, &Unavailable{fmt.Errorf("native rule test did not confirm execution")}
	}
	result.Passed = true
	return result, nil
}
