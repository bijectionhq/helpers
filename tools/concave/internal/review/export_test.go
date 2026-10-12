package review_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/review"
	"concave/internal/testrepo"
)

func TestExportExcludesPeersEditInSameFileAndRechecksCandidate(t *testing.T) {
	f := testrepo.New(t)
	// A native source gives this fixture independent ownership of its two edits.
	f.Write("packaging/own.go", "package p\n\nfunc own() {}\n\nfunc peer() {}\n")
	f.Config.Checks = map[string]policy.CheckDefinition{"local": {Command: []string{"sh", "-c", `printf '%s\n' '{"Action":"pass","Package":"pkg","Test":"TestOwn"}'`}, Directory: ".", Inputs: []string{"packaging/own.go"}, Toolchain: []policy.ToolCheck{{Command: []string{"sh", "-c", "printf v1"}, Expected: "v1"}}, TimeoutSeconds: 5, Results: "go-test-json", Witnesses: map[string][]string{"packaging/own.go": {"pkg::TestOwn"}}}}
	f.Save()
	f.Write("scripts/land.py", `import base64,json,subprocess,sys
files={}
for p in sys.argv[2:]:
 b=subprocess.check_output(['git','show','HEAD:'+p])
 oid=subprocess.check_output(['git','rev-parse','HEAD:'+p]).decode().strip()
 files[p]={'identity':'100644:'+oid,'data':base64.b64encode(b).decode()}
print(json.dumps({'format':1,'kind':'land-baselines','head':'fixture','files':files}))
`)
	f.Commit()
	f.Write("packaging/own.go", "package p\n\nfunc own() { local() }\n\nfunc peer() { other() }\n")
	repo, config := f.Open()
	packet, err := analysis.ReviewContracts(repo, config, analysis.Options{Paths: []string{"packaging/own.go"}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := review.Compose(repo, config, packet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hunks) != 2 {
		t.Fatal(r.Hunks)
	}
	r.Selected = []string{r.Hunks[0].ID}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	result, err := review.SelectContent(repo, config, r, filepath.Join(f.Root, "target/export"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Files[0].Content.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "local()") || strings.Contains(string(data), "other()") {
		t.Fatal(string(data))
	}
	working, _ := os.ReadFile(filepath.Join(f.Root, "packaging/own.go"))
	if !strings.Contains(string(working), "other()") {
		t.Fatal("export edited peer")
	}

}

func TestExportCreatesFileAndRejectsChangedBaseline(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent baseline", true: "stale baseline"}[stale], func(t *testing.T) {
			f := testrepo.New(t)
			baseline := ""
			if stale {
				baseline = "unexpected source"
			}
			f.Write("scripts/land.py", "import base64,json,sys\nprint(json.dumps({'format':1,'kind':'land-baselines','head':'fixture','files':{p:{'identity':'absent','data':base64.b64encode("+strconv.Quote(baseline)+".encode()).decode()} for p in sys.argv[2:]}}))\n")
			f.Commit()
			f.Write("packaging/new.go", "package own\n")
			repo, config := f.Open()
			packet, err := analysis.ReviewContracts(repo, config, analysis.Options{Paths: []string{"packaging/new.go"}}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			r, err := review.Compose(repo, config, packet, nil)
			if err != nil || len(r.Hunks) != 1 {
				t.Fatal(r, err)
			}
			r.Selected = []string{r.Hunks[0].ID}
			if err := r.Seal(); err != nil {
				t.Fatal(err)
			}
			result, err := review.SelectContent(repo, config, r, filepath.Join(f.Root, "target/export"))
			if stale {
				if err == nil || !strings.Contains(err.Error(), "landing baseline differs") {
					t.Fatal(result, err)
				}
				return
			}
			if err != nil || result.Files[0].Baseline != "absent" {
				t.Fatal(result, err)
			}

		})
	}
}
