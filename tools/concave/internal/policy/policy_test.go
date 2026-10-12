package policy_test

import (
	"strings"
	"testing"

	"concave/internal/artifact"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestFenceIsBounded(t *testing.T) {
	before := []byte("one\ntwo\n")
	body := "// why: retain the invariant\n// witness: tests::hook\nhook();\n"
	after := []byte("one\n// concave:begin hook\n" + body + "// concave:end hook\ntwo\n")
	c := policy.Config{Exceptions: []policy.Exception{{ID: "hook", Path: "core.rs", Start: 1, End: 1, UpstreamSHA256: repository.Digest(before), ReplacementSHA256: repository.Digest([]byte("hook();\n")), Fence: true}}}
	class, _, fs := c.Classify("core.rs", before, after, nil)
	if class != "excepted" || len(fs) > 0 {
		t.Fatal(class, fs)
	}
	for _, s := range []string{strings.Replace(string(after), "hook();", "bypass();", 1), string(after) + "extra\n", strings.Replace(string(after), "end hook", "end other", 1), strings.Replace(string(after), "begin hook", "begin unknown", 1), strings.Replace(string(after), body, "// concave:begin nested\n"+body, 1), strings.Replace(string(after), "// concave:end hook\n", "", 1)} {
		class, _, _ := c.Classify("core.rs", before, []byte(s), nil)
		if class != "unexplained" {
			t.Fatal("fence escaped", s)
		}
	}
	class, _, _ = c.Classify("core.rs", []byte("upstream changed\n"), after, nil)
	if class != "unexplained" {
		t.Fatal("stale upstream accepted")
	}
}
func TestHunkExceptionIncludingDeletion(t *testing.T) {
	for _, after := range []string{"replacement\n", ""} {
		before := []byte("original\n")
		h := repository.Hunk{Start: 0, End: 1, After: after}
		c := policy.Config{Exceptions: []policy.Exception{{ID: "fix", Path: "x", Start: 0, End: 1, UpstreamSHA256: repository.Digest(before), ReplacementSHA256: repository.Digest([]byte(after))}}}
		class, _, fs := c.Classify("x", before, []byte(after), []repository.Hunk{h})
		if class != "excepted" || len(fs) > 0 {
			t.Fatal(fs)
		}
		h.After = "different\n"
		class, _, _ = c.Classify("x", before, []byte(h.After), []repository.Hunk{h})
		if class != "unexplained" {
			t.Fatal("fingerprint ignored")
		}
	}
}
func TestScopedManifestTransformation(t *testing.T) {
	c := policy.Config{Manifest: policy.Manifest{Rewrites: map[string][][2]string{"module.js": {{"import 'convex'", "import 'bijection'"}}}}, Transforms: []policy.Transform{{ID: "package", Paths: []string{"module.ts"}, Module: "module.js"}}}
	before := []byte("import 'convex';\nconst wire = 'convex';\n")
	after := []byte("import 'bijection';\nconst wire = 'convex';\n")
	class, _, fs := c.Classify("module.ts", before, after, nil)
	if class != "transformed" || len(fs) > 0 {
		t.Fatal(class, fs)
	}
	class, _, _ = c.Classify("module.ts", before, []byte(strings.ReplaceAll(string(after), "'convex'", "'bijection'")), nil)
	if class != "unexplained" {
		t.Fatal("wire rename accepted")
	}
	class, _, _ = c.Classify("elsewhere.ts", before, after, nil)
	if class != "unexplained" {
		t.Fatal("scope escaped")
	}
}
func TestValidation(t *testing.T) {
	for _, kind := range []string{"duplicate selector", "duplicate exact", "unsafe root", "unknown field", "overlap", "exact exception", "missing reason", "bad case", "invalid format"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			switch kind {
			case "duplicate selector":
				f.Config.Families[1].Paths = append(f.Config.Families[1].Paths, "crates/")
			case "duplicate exact":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, f.Config.PreserveExactly[0])
			case "unsafe root":
				f.Config.Roots = []string{"../elsewhere"}
			case "unknown field":
				var v policy.Config
				if e := artifact.Decode([]byte(`{"unknown":true}`), &v); e == nil {
					t.Fatal("accepted unknown field")
				}
				return
			case "invalid format":
				f.Config.Format = 1
			case "bad case":
				f.Config.Cases["invalid"] = policy.Case{}
			default:
				x := policy.Exception{ID: "a", Path: "crates/core/lib.rs", Start: 0, End: 1, UpstreamSHA256: strings.Repeat("a", 64), ReplacementSHA256: strings.Repeat("b", 64), Reason: "reason", Witnesses: []string{"test"}}
				if kind == "exact exception" {
					x.Path = f.Config.PreserveExactly[0]
				}
				if kind == "missing reason" {
					x.Reason = ""
				}
				f.Config.Exceptions = []policy.Exception{x}
				if kind == "overlap" {
					x.ID = "b"
					f.Config.Exceptions = append(f.Config.Exceptions, x)
				}
			}
			if e := f.Config.Validate(); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
func TestManifestParticipatesInPolicyIdentity(t *testing.T) {
	f := testrepo.New(t)
	f.Config.TransformationManifest = "mapping.json"
	f.Write("mapping.json", `{"contract_names":[],"rewrites":{"x":[["old","new"]]}}`)
	f.Save()
	_, a := f.Open()
	f.Write("mapping.json", `{"contract_names":[],"rewrites":{"x":[["old","different"]]}}`)
	_, b := f.Open()
	if a.SHA256 == b.SHA256 {
		t.Fatal("manifest identity omitted")
	}
}
func TestCommittedPolicyNeverUsesDirtyConfig(t *testing.T) {
	f := testrepo.New(t)
	ref := f.Git("rev-parse", "HEAD")
	r, c := f.Open()
	f.Config.PreserveExactly = nil
	f.Save()
	committed, e := policy.Load(r, ref)
	if e != nil {
		t.Fatal(e)
	}
	if committed.SHA256 != c.SHA256 {
		t.Fatal("dirty config used")
	}
	_, working := f.Open()
	if working.SHA256 == committed.SHA256 {
		t.Fatal("fixture did not change")
	}
}
func TestStaleTransformRefusesMissingSource(t *testing.T) {
	c := policy.Config{Transforms: []policy.Transform{{ID: "t", Paths: []string{"x"}, Module: "x"}}, Manifest: policy.Manifest{Rewrites: map[string][][2]string{"x": {{"absent", "replacement"}}}}}
	class, _, _ := c.Classify("x", []byte("same"), []byte("same"), nil)
	if class != "unexplained" {
		t.Fatal(class)
	}
}

func TestStrictJSON(t *testing.T) {
	for _, text := range []string{`{"format":1,"format":2}`, `{"cases":{"x":{},"x":{}}}`, `{} {}`} {
		var v policy.Config
		if err := artifact.Decode([]byte(text), &v); err == nil {
			t.Fatal("accepted ambiguous JSON", text)
		}
	}
	for _, text := range []string{`{"rewrites":{"x":[["a"]]}}`, `{"rewrites":{"x":[["a","b","c"]]}}`, `{"unknown":true}`} {
		var v policy.Manifest
		if err := artifact.Decode([]byte(text), &v); err == nil {
			t.Fatal("accepted invalid manifest", text)
		}
	}
}
