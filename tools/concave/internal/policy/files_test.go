package policy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/artifact"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func fileException() policy.Exception {
	return policy.Exception{ID: "asset", Path: "crates/core/logo.png", File: &policy.FileBinding{
		Upstream:    policy.FileIdentity{SHA256: repository.Digest([]byte("old")), Mode: "100644"},
		Replacement: &policy.FileIdentity{SHA256: repository.Digest([]byte("new")), Mode: "100644"},
	}, Reason: "owned branding", Witnesses: []string{"owner.ts"}}
}
func TestFileBindingValidation(t *testing.T) {
	for _, kind := range []string{"valid", "retirement", "same bytes", "bad hash", "symlink", "mode", "span", "fence", "missing reason", "exact", "transform", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			f := testrepo.New(t)
			x := fileException()
			switch kind {
			case "retirement":
				x.File.Replacement = nil
			case "same bytes":
				*x.File.Replacement = x.File.Upstream
			case "bad hash":
				x.File.Upstream.SHA256 = "unknown"
			case "symlink":
				x.File.Upstream.Mode = "120000"
			case "mode":
				x.File.Replacement.Mode = "100755"
			case "span":
				x.End = 1
			case "fence":
				x.Fence = true
			case "missing reason":
				x.Reason = ""
			case "exact":
				f.Config.PreserveExactly = append(f.Config.PreserveExactly, x.Path)
			case "transform":
				f.Config.Transforms = []policy.Transform{{ID: "branding", Paths: []string{x.Path}, Replacements: []policy.Replacement{{From: "old", To: "new"}}, Explanation: policy.Explanation{Why: "branding", Witnesses: []string{"owner.ts"}}}}
			}
			f.Config.Exceptions = []policy.Exception{x}
			if kind == "overlap" {
				y := fileException()
				y.ID = "second"
				f.Config.Exceptions = append(f.Config.Exceptions, y)
			}
			err := f.Config.Validate()
			if valid := kind == "valid" || kind == "retirement"; (err == nil) != valid {
				t.Fatal(kind, err)
			}
		})
	}
}
func TestFileBindingDecodingAndEncoding(t *testing.T) {
	x := fileException()
	x.Preserves = []string{"owner.ts#branding"}
	for _, replacement := range []*policy.FileIdentity{x.File.Replacement, nil} {
		x.File.Replacement = replacement
		b, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"start"`) || strings.Contains(string(b), `"fence"`) || !strings.Contains(string(b), `"replacement":`) {
			t.Fatal(string(b))
		}
		var round policy.Exception
		if err = artifact.Decode(b, &round); err != nil || round.File == nil || strings.Join(round.Preserves, ",") != "owner.ts#branding" || strings.Contains(string(b), `"contract"`) {
			t.Fatal(round, err)
		}
	}
	b, err := json.Marshal(policy.Exception{ID: "span", Start: 0, End: 0})
	if err != nil || !strings.Contains(string(b), `"start":0`) || !strings.Contains(string(b), `"end":0`) {
		t.Fatal(string(b), err)
	}
	for _, data := range []string{
		`{"upstream":{}}`,
		`{"upstream":{},"replacement":null,"extra":true}`,
		`{"upstream":{},"replacement":null,"replacement":null}`,
		`{"upstream":{},"replacement":{"sha256":"x","mode":"100644","extra":true}}`,
	} {
		var v policy.FileBinding
		if err := artifact.Decode([]byte(data), &v); err == nil {
			t.Fatal("invalid binding decoded", data)
		}
	}
}

func TestFileExceptionLookupAllocatesOnlyForItsResult(t *testing.T) {
	cfg := &policy.Config{Exceptions: make([]policy.Exception, 256)}
	for i := range cfg.Exceptions {
		cfg.Exceptions[i] = policy.Exception{Path: "unrelated.rs"}
	}
	cfg.Exceptions[255] = fileException()
	if n := testing.AllocsPerRun(1, func() {
		if cfg.FileException("missing.rs") != nil {
			t.Fatal("unexpected exception")
		}
	}); n != 0 {
		t.Fatalf("missing lookup allocated %g objects", n)
	}
	found := cfg.FileException(cfg.Exceptions[255].Path)
	if found == nil || found.ID != "asset" {
		t.Fatal(found)
	}
	found.ID = "caller edit"
	if cfg.Exceptions[255].ID != "asset" {
		t.Fatal("lookup exposed mutable policy")
	}
}
