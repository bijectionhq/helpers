package policy

import "fmt"

// A single Cargo library harness supplies the otherwise absent libtest target
// identity. Keep this deliberately narrow until another real target is needed.
func validateRustTarget(check CheckDefinition) error {
	args := check.Command
	cargo := -1
	for i, arg := range args {
		if arg == "cargo" {
			if cargo >= 0 {
				return fmt.Errorf("Rust check needs one Cargo invocation")
			}
			cargo = i
		}
	}
	if cargo < 0 || cargo+1 >= len(args) || args[cargo+1] != "test" {
		return fmt.Errorf("Rust check requires cargo test")
	}
	pkg := ""
	lib, locked, format, threads := false, false, false, false
	separator := false
	for i := cargo + 2; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if separator {
				return fmt.Errorf("repeated Rust argument separator")
			}
			separator = true
			continue
		}
		if separator {
			if a == "--format=json" {
				format = true
			}
			if a == "--test-threads=1" {
				threads = true
			}
			continue
		}
		switch a {
		case "-p", "--package":
			if pkg != "" || i+1 >= len(args) {
				return fmt.Errorf("Rust check requires one package")
			}
			i++
			pkg = args[i]
		case "--lib":
			lib = true
		case "--locked":
			locked = true
		case "--workspace", "--all", "--tests", "--all-targets", "--test", "--bins", "--bin", "--benches", "--bench", "--doc":
			return fmt.Errorf("Rust check requires one explicit library harness")
		}
	}
	if pkg == "" || !lib || !locked || !format || !threads || check.Target != pkg+"/lib" {
		return fmt.Errorf("Rust check requires --locked -p PACKAGE --lib -- --test-threads=1 --format=json and target PACKAGE/lib")
	}
	return nil
}
