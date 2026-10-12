package coverage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"concave/internal/process"
)

// command runs argv in dir and returns its combined, bounded output.
func command(ctx context.Context, dir string, env []string, argv ...string) ([]byte, error) {
	result, err := process.Run(process.WithEnvironment(ctx, env...), dir, argv, nil, 32<<20)
	return append(result.Stdout, result.Stderr...), err
}

// owner walks up from file to the directory holding marker.
func owner(root, file, marker string) (string, error) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), marker)); err == nil {
			return dir, nil
		}
		if dir == "." || dir == "/" {
			return "", fmt.Errorf("no %s owns %s", marker, file)
		}
	}
}

// runGo runs one Go test (TestName or TestName/subtest) with a coverage
// profile of the fenced file's package and returns per-line counts for it.
func runGo(ctx context.Context, o Options, r Request) (map[int]int64, []string, error) {
	module, err := owner(o.Root, r.WitnessPath, "go.mod")
	if err != nil {
		return nil, nil, err
	}
	if fence, err := owner(o.Root, r.Path, "go.mod"); err != nil || fence != module {
		return nil, nil, fmt.Errorf("fence %s is outside the witness module %s", r.Path, module)
	}
	moduleDir := filepath.Join(o.Root, filepath.FromSlash(module))
	rel := func(p string) string {
		d, _ := filepath.Rel(moduleDir, filepath.Join(o.Root, filepath.FromSlash(path.Dir(p))))
		return "./" + filepath.ToSlash(d)
	}
	list, err := command(ctx, moduleDir, nil, "go", "list", "-f", "{{.ImportPath}}", rel(r.Path))
	if err != nil {
		return nil, nil, fail("go list", list, err)
	}
	importPath := strings.TrimSpace(string(list))
	parts := strings.Split(r.Symbol, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	profile := filepath.Join(o.Scratch, "go-cover.out")
	argv := []string{"go", "test", "-count=1", "-run", strings.Join(parts, "/"), "-coverpkg=" + importPath, "-coverprofile=" + profile, rel(r.WitnessPath)}
	out, err := command(ctx, moduleDir, nil, argv...)
	if err != nil {
		return nil, argv, fail("go test", out, err)
	}
	if bytes.Contains(out, []byte("no tests to run")) {
		return nil, argv, &runError{message: "the witness test did not run", log: tail(out)}
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		return nil, argv, err
	}
	counts := map[int]int64{}
	name := importPath + "/" + path.Base(r.Path) + ":"
	block := regexp.MustCompile(`^(.+):(\d+)\.\d+,(\d+)\.\d+ \d+ (\d+)$`)
	for _, line := range strings.Split(string(data), "\n") {
		m := block.FindStringSubmatch(line)
		if m == nil || m[1]+":" != name {
			continue
		}
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		count, _ := strconv.ParseInt(m[4], 10, 64)
		for l := start; l <= end; l++ {
			counts[l] += count
		}
	}
	return counts, argv, nil
}

// rustTest names one libtest case: its package, cargo target and full test path.
type rustTest struct {
	crate, pkg, test string
	target           []string
}

func rustTarget(root string, r Request) (rustTest, error) {
	crate, err := owner(root, r.WitnessPath, "Cargo.toml")
	if err != nil {
		return rustTest{}, err
	}
	manifest, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(crate), "Cargo.toml"))
	if err != nil {
		return rustTest{}, err
	}
	m := regexp.MustCompile(`(?ms)^\[package\][^\[]*?^name\s*=\s*"([^"]+)"`).FindSubmatch(manifest)
	if m == nil {
		return rustTest{}, fmt.Errorf("%s/Cargo.toml names no package", crate)
	}
	t := rustTest{crate: crate, pkg: string(m[1]), test: r.Symbol, target: []string{"--lib"}}
	inCrate := strings.TrimPrefix(r.WitnessPath, crate+"/")
	switch {
	case strings.HasPrefix(inCrate, "tests/") && strings.Count(inCrate, "/") == 1:
		t.target = []string{"--test", strings.TrimSuffix(path.Base(inCrate), ".rs")}
	case strings.HasPrefix(inCrate, "src/") && !strings.HasPrefix(inCrate, "src/bin/"):
		module := strings.TrimSuffix(strings.TrimPrefix(inCrate, "src/"), ".rs")
		module = strings.TrimSuffix(strings.TrimSuffix(module, "/mod"), "lib")
		module = strings.Trim(strings.ReplaceAll(module, "/", "::"), ":")
		if module != "" {
			t.test = module + "::" + r.Symbol
		}
	default:
		return rustTest{}, fmt.Errorf("no test target for %s", r.WitnessPath)
	}
	return t, nil
}

func rustHost() string {
	return map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] + map[string]string{"darwin": "-apple-darwin", "linux": "-unknown-linux-gnu"}[runtime.GOOS]
}

// rustFlags are the configured target rustflags, which RUSTFLAGS would replace.
func rustFlags(ctx context.Context, root string) ([]string, error) {
	config, err := command(ctx, root, nil, "cargo", "-Zunstable-options", "config", "get", "--format", "json", "target")
	flags := []string{}
	if err != nil {
		return flags, nil
	}
	var parsed struct {
		Target map[string]struct {
			Rustflags []string `json:"rustflags"`
		} `json:"target"`
	}
	if json.Unmarshal(config, &parsed) != nil {
		return nil, fmt.Errorf("cannot read cargo target rustflags")
	}
	for key, value := range parsed.Target {
		switch {
		case key == "cfg(all())" || key == rustHost():
			flags = append(flags, value.Rustflags...)
		case strings.HasPrefix(key, "cfg("):
			return nil, fmt.Errorf("cargo rustflags for %s cannot be combined with extra flags", key)
		}
	}
	return flags, nil
}

// buildRust compiles the test target in tree through the build slot, into the
// scratch target, and returns its test executable. A compile failure is a
// *runError carrying the compiler output.
func buildRust(ctx context.Context, o Options, tree string, t rustTest, flags []string) (string, []string, error) {
	env := []string{"CARGO_TARGET_DIR=" + filepath.Join(o.Scratch, "target"), "RUSTFLAGS=" + strings.Join(flags, " "), "LLVM_PROFILE_FILE=" + filepath.Join(o.Scratch, "build-%p-%m.profraw")}
	argv := append([]string{"python3", filepath.Join(o.Root, "scripts", "build_slot.py"), "--", "env"}, env...)
	argv = append(argv, append(append([]string{"cargo", "test", "-p", t.pkg}, t.target...), "--no-run", "--message-format=json")...)
	out, err := command(ctx, tree, nil, argv...)
	if err != nil {
		return "", argv, fail("build", out, err)
	}
	executable := ""
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var message struct {
			Reason     string `json:"reason"`
			Executable string `json:"executable"`
			Profile    struct {
				Test bool `json:"test"`
			} `json:"profile"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) == nil && message.Reason == "compiler-artifact" && message.Executable != "" && message.Profile.Test {
			executable = message.Executable
		}
	}
	if executable == "" {
		return "", argv, &runError{message: "the build produced no test executable", log: tail(out)}
	}
	return executable, argv, nil
}

// runRustTest runs exactly the witness case, under the repository's
// PostgreSQL cluster owner when the case needs one. passed reports one
// passing case; a failing or panicking case returns passed false and no error.
// err reports a run that could not decide the case.
func runRustTest(ctx context.Context, o Options, tree string, t rustTest, executable string, env []string) (bool, []string, []byte, error) {
	crate := filepath.Join(tree, filepath.FromSlash(t.crate))
	run := []string{executable, "--exact", t.test, "--test-threads=1"}
	out, err := command(ctx, crate, append(env, "CARGO_MANIFEST_DIR="+crate), run...)
	if err != nil && bytes.Contains(out, []byte("select an owned PostgreSQL test cluster with CONVEX_TEST_POSTGRES_URL")) && os.Getenv("CONVEX_TEST_POSTGRES_URL") == "" {
		report, made := os.MkdirTemp(o.Scratch, "postgres-")
		if made != nil {
			return false, nil, nil, made
		}
		_ = os.Remove(report) // the cluster owner creates its own new report directory
		run = []string{"python3", filepath.Join(o.Root, "scripts", "test_persistence.py"), "postgres", "--test-binary", executable, "--test-filter", t.test, "--require", t.test, "--test-threads", "1", "--output", report}
		out, err = command(ctx, o.Root, env, run...)
		switch {
		case ctx.Err() != nil:
			return false, run, out, ctx.Err()
		case err == nil && bytes.Contains(out, []byte("1 tests passed")):
			return true, run, out, nil
		case err == nil:
			return false, run, out, &runError{message: "the PostgreSQL witness did not run exactly once", log: tail(out)}
		case bytes.Contains(out, []byte("FAILED")) || bytes.Contains(out, []byte("failed")):
			return false, run, out, nil
		}
		return false, run, out, fail("PostgreSQL witness run", out, err)
	}
	switch {
	case ctx.Err() != nil:
		return false, run, out, ctx.Err()
	case err == nil && bytes.Contains(out, []byte("test result: ok. 1 passed")):
		return true, run, out, nil
	case bytes.Contains(out, []byte("test result: FAILED. 0 passed; 1 failed")):
		return false, run, out, nil
	case err == nil:
		return false, run, out, &runError{message: "the witness test did not run exactly once", log: tail(out)}
	}
	return false, run, out, fail("witness test", out, err)
}

// runRust builds the witness crate's test target with -C instrument-coverage
// in the private scratch target, runs exactly that test, and reads line counts
// for the fenced file with the pinned toolchain's llvm-tools.
func runRust(ctx context.Context, o Options, r Request) (map[int]int64, []string, error) {
	sysroot, err := command(ctx, o.Root, nil, "rustc", "--print", "sysroot")
	if err != nil {
		return nil, nil, fail("rustc --print sysroot", sysroot, err)
	}
	tools := filepath.Join(strings.TrimSpace(string(sysroot)), "lib", "rustlib", rustHost(), "bin")
	for _, tool := range []string{"llvm-profdata", "llvm-cov"} {
		if _, err := os.Stat(filepath.Join(tools, tool)); err != nil {
			return nil, nil, &unavailableError{fmt.Sprintf("%s is not installed for the pinned toolchain; install its llvm-tools component", tool)}
		}
	}
	t, err := rustTarget(o.Root, r)
	if err != nil {
		return nil, nil, err
	}
	flags, err := rustFlags(ctx, o.Root)
	if err != nil {
		return nil, nil, err
	}
	executable, argv, err := buildRust(ctx, o, o.Root, t, append(flags, "-Cinstrument-coverage"))
	if err != nil {
		return nil, argv, err
	}
	profiles := filepath.Join(o.Scratch, "profiles")
	_ = os.RemoveAll(profiles)
	passed, run, out, err := runRustTest(ctx, o, o.Root, t, executable, []string{"LLVM_PROFILE_FILE=" + filepath.Join(profiles, "%p-%m.profraw")})
	if err != nil {
		return nil, run, err
	}
	if !passed {
		return nil, run, &runError{message: "witness test failed", log: tail(out)}
	}
	raw, _ := filepath.Glob(filepath.Join(profiles, "*.profraw"))
	if len(raw) == 0 {
		return nil, run, fmt.Errorf("the witness test wrote no coverage profile")
	}
	merged := filepath.Join(o.Scratch, "witness.profdata")
	if out, err := command(ctx, o.Root, nil, append([]string{filepath.Join(tools, "llvm-profdata"), "merge", "-sparse", "-o", merged}, raw...)...); err != nil {
		return nil, run, fail("llvm-profdata", out, err)
	}
	source := filepath.Join(o.Root, filepath.FromSlash(r.Path))
	lcov, err := command(ctx, o.Root, nil, filepath.Join(tools, "llvm-cov"), "export", "-format=lcov", "-instr-profile="+merged, executable, source)
	if err != nil {
		return nil, run, fail("llvm-cov", lcov, err)
	}
	counts := map[int]int64{}
	current := ""
	for _, line := range strings.Split(string(lcov), "\n") {
		if name, ok := strings.CutPrefix(line, "SF:"); ok {
			current = name
			continue
		}
		if data, ok := strings.CutPrefix(line, "DA:"); ok && sameFile(current, source) {
			fields := strings.Split(data, ",")
			if len(fields) >= 2 {
				n, _ := strconv.Atoi(fields[0])
				c, _ := strconv.ParseInt(fields[1], 10, 64)
				counts[n] += c
			}
		}
	}
	return counts, run, nil
}

func sameFile(a, b string) bool {
	x, err1 := filepath.EvalSymlinks(a)
	y, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && x == y
}
