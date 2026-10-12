package policy

import (
	"fmt"
	"slices"
	"strings"

	"concave/internal/repository"
)

type ToolCheck struct {
	Command     []string `json:"command"`
	Expected    string   `json:"expected,omitempty"`
	VersionFile string   `json:"version_file,omitempty"`
}
type CheckDefinition struct {
	Variables      map[string]string   `json:"variables,omitempty"`
	Paths          []string            `json:"paths,omitempty"`
	Environment    string              `json:"environment,omitempty"`
	ReuseReason    string              `json:"reuse_reason,omitempty"`
	Target         string              `json:"target,omitempty"`
	Witnesses      map[string][]string `json:"witnesses,omitempty"`
	Command        []string            `json:"command"`
	Directory      string              `json:"directory"`
	Inputs         []string            `json:"inputs"`
	Toolchain      []ToolCheck         `json:"toolchain"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	Results        string              `json:"results"`
}

// Environment prepares one candidate-owned pnpm workspace for its named checks.
// Version pins and installation policy remain in the repository's tool files.
// Includes names other environments prepared and bound with it, for a check
// whose tests use more than one workspace; they include nothing themselves.
type Environment struct {
	Directory      string   `json:"directory"`
	Packages       []string `json:"packages"`
	Includes       []string `json:"includes,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// EnvironmentClosure is an environment and the environments it includes.
func (c *Config) EnvironmentClosure(name string) []string {
	if name == "" {
		return nil
	}
	return append([]string{name}, c.Environments[name].Includes...)
}

func (c *Config) validateChecks() error {
	for name, environment := range c.Environments {
		if !idPattern.MatchString(name) || !repository.ValidPath(environment.Directory) || len(environment.Packages) == 0 || len(environment.Packages) > 32 || environment.TimeoutSeconds < 1 || environment.TimeoutSeconds > 1800 {
			return fmt.Errorf("environment %s needs a workspace, packages and timeout 1..1800 seconds", name)
		}
		seen := map[string]bool{}
		for _, path := range environment.Packages {
			if !repository.ValidPath(path) || strings.HasSuffix(path, "/") || seen[path] {
				return fmt.Errorf("environment %s has invalid or duplicate package %q", name, path)
			}
			seen[path] = true
		}
		for i, include := range environment.Includes {
			if other, ok := c.Environments[include]; !ok || include == name || len(other.Includes) > 0 || slices.Contains(environment.Includes[:i], include) {
				return fmt.Errorf("environment %s includes an unknown, nested or repeated environment %q", name, include)
			}
		}
	}
	argv := func(args []string) bool {
		if len(args) == 0 || len(args) > 128 {
			return false
		}
		for _, arg := range args {
			if strings.ContainsRune(arg, 0) {
				return false
			}
		}
		return args[0] != ""
	}
	for name, check := range c.Checks {
		if check.Environment != "" {
			if _, ok := c.Environments[check.Environment]; !ok {
				return fmt.Errorf("check %s references unknown environment %s", name, check.Environment)
			}
		}
		if check.ReuseReason != "" && strings.TrimSpace(check.ReuseReason) == "" {
			return fmt.Errorf("check %s needs a substantive reuse dependency-boundary reason", name)
		}
		if !idPattern.MatchString(name) || !argv(check.Command) || check.Directory != "." && !repository.ValidPath(check.Directory) || len(check.Inputs) == 0 || len(check.Toolchain) == 0 || check.TimeoutSeconds < 1 || check.TimeoutSeconds > 1800 {
			return fmt.Errorf("check %s needs command, directory, inputs, toolchain and timeout 1..1800 seconds", name)
		}
		seenPaths := map[string]bool{}
		for _, path := range check.Paths {
			if !repository.ValidPath(path) || seenPaths[path] || !repository.Matches(path, check.Inputs) {
				return fmt.Errorf("check %s needs unique source paths covered by its inputs: %q", name, path)
			}
			seenPaths[path] = true
		}
		if len(check.Paths) > 0 && len(check.Witnesses) == 0 && check.Results != "command" {
			return fmt.Errorf("check %s needs required witness cases for its source paths", name)
		}
		for _, p := range check.Inputs {
			if !repository.ValidPath(p) {
				return fmt.Errorf("check %s has unsafe input %s", name, p)
			}
		}
		switch check.Results {
		case "command", "go-test-json", "vitest-json", "jest-json", "node-test-tap", "rust-test-json", "python-unittest":
		default:
			return fmt.Errorf("check %s has unknown result format", name)
		}
		if check.Results == "rust-test-json" && (check.Target == "" || strings.ContainsAny(check.Target, "\n\r\x00")) {
			return fmt.Errorf("check %s needs one explicit Rust package/target identity", name)
		}
		if check.Results == "rust-test-json" {
			if err := validateRustTarget(check); err != nil {
				return fmt.Errorf("check %s: %w", name, err)
			}
		}
		for witness, cases := range check.Witnesses {
			path, _, err := WitnessTarget("", witness)
			if err != nil {
				return fmt.Errorf("check %s needs repository-relative witness paths: %w", name, err)
			}
			if !repository.Matches(path, check.Inputs) {
				return fmt.Errorf("check %s does not bind its witness source %s", name, path)
			}
			if strings.TrimSpace(witness) == "" || len(cases) == 0 || check.Results == "command" {
				return fmt.Errorf("check %s needs an explicit witness and test cases", name)
			}
			seen := map[string]bool{}
			for _, id := range cases {
				if strings.TrimSpace(id) == "" || seen[id] {
					return fmt.Errorf("check %s has an empty or duplicate required case", name)
				}
				seen[id] = true
			}
		}
		// A fence reference holds no whitespace: the annotation parser drops
		// what a formatter's wrap introduced. A key written with the spaces of
		// a test title names that same reference.
		if len(check.Witnesses) > 0 {
			references := make(map[string][]string, len(check.Witnesses))
			for witness, cases := range check.Witnesses {
				reference := strings.Join(strings.Fields(witness), "")
				if _, duplicate := references[reference]; duplicate {
					return fmt.Errorf("check %s maps the witness %s more than once", name, reference)
				}
				references[reference] = cases
			}
			check.Witnesses = references
			c.Checks[name] = check
		}
		for key, value := range check.Variables {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return fmt.Errorf("check %s has invalid execution variable %q", name, key)
			}
		}
		for _, tool := range check.Toolchain {
			if !argv(tool.Command) || tool.Expected != "" && tool.VersionFile != "" || tool.VersionFile != "" && !repository.ValidPath(tool.VersionFile) {
				return fmt.Errorf("check %s needs each tool's identity probe and at most one expected/version_file", name)
			}
		}
	}
	return nil
}
