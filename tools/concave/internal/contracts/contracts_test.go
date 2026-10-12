package contracts_test

import (
	"concave/internal/contracts"
	"concave/internal/syntax"
	"context"
	"path/filepath"
	"testing"
)

func engine(t *testing.T) *syntax.Engine {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return syntax.New(context.Background(), root)
}
func TestDeclarationScopes(t *testing.T) {
	cases := []struct {
		path, source string
		names        []string
	}{
		{"owner.rs", "// concave:invariant outer\n// Outer obligation.\nmod inner {\n// concave:invariant first\n// First obligation.\n// concave:invariant second\n// Second obligation.\n#[test]\nfn check() {}\n}\n", []string{"inner", "check", "check"}},
		{"owner.ts", "// concave:invariant outer\n// Outer obligation.\nexport class Owner {\n// concave:invariant first\n// First obligation.\ncheck(): void {}\n}\n", []string{"Owner", "check"}},
		{"owner.tsx", "// concave:invariant outer\n// Outer obligation.\nexport const View = () => <span/>;\n", []string{"View"}},
		{"owner.go", "package fixture\n// concave:invariant outer\n// Outer obligation.\nfunc Check() {}\n", []string{"Check"}},
		{"type.go", "package fixture\n// concave:invariant outer\n// Outer obligation.\ntype Owner struct {}\n", []string{"Owner"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			doc, err := contracts.Parse(engine(t), tc.path, []byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Invariants) != len(tc.names) {
				t.Fatalf("%+v", doc)
			}
			for i, name := range tc.names {
				if doc.Invariants[i].Declaration.Name != name {
					t.Fatal(doc.Invariants)
				}
			}
			last := doc.Invariants[len(doc.Invariants)-1]
			applicable := doc.Applicable(last.Declaration.Range.Start.Line+1, 0)
			if len(applicable) != len(tc.names) {
				t.Fatalf("scope: %+v", applicable)
			}
		})
	}
}
func TestMalformedOrUnattachedInvariantsFail(t *testing.T) {
	for _, source := range []string{
		"// concave:invariant\n// obligation\nfn f() {}",
		"// concave:invariant x\nfn f() {}",
		"// concave:invariant x\n// obligation",
		"// concave:invariant x\n// one\nfn f() {}\n// concave:invariant x\n// two\nfn g() {}",
		"// concave:invariant x\n// obligation\nfn f( {",
	} {
		if _, err := contracts.Parse(engine(t), "owner.rs", []byte(source)); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}

func TestRustWitnessDefinitionsAndImplementationScopes(t *testing.T) {
	source := []byte(`mod snapshots {
 struct Snapshot;
 impl Snapshot { fn new() {} }
 impl Read for Snapshot { fn read() {} fn shared() {} }
 impl Write for Snapshot { fn shared() {} }
 enum State { Ready }
 impl State { fn ready() {} }
 // The type definition lives elsewhere; only the implementation is local.
 impl Imported { fn local() {} }
}
mod first { struct Duplicate; impl Duplicate { fn one() {} } }
mod second { struct Duplicate; impl Duplicate { fn two() {} } }
`)
	parser := engine(t)
	resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, parser)
	for _, test := range []struct{ symbol, resolution string }{
		{"Snapshot", "symbol_found"},
		{"snapshots::Snapshot", "symbol_found"},
		{"Snapshot::new", "symbol_found"},
		{"snapshots::Snapshot::read", "symbol_found"},
		{"Snapshot::shared", "symbol_ambiguous"},
		{"State", "symbol_found"},
		{"State::ready", "symbol_found"},
		{"Imported", "symbol_not_resolved"},
		{"Imported::local", "symbol_found"},
		{"Duplicate", "symbol_ambiguous"},
		{"first::Duplicate", "symbol_found"},
		{"second::Duplicate", "symbol_found"},
		{"first::Duplicate::one", "symbol_found"},
	} {
		t.Run(test.symbol, func(t *testing.T) {
			status, err := resolver.Witness("owner.rs", test.symbol)
			if err != nil || status != test.resolution {
				t.Fatal(status, err)
			}
		})
	}
	if parser.Calls != 2 {
		t.Fatal("witness lookup must share one parsed document", parser.Calls)
	}
}

func TestImplementationRemainsAnInvariantScope(t *testing.T) {
	source := []byte(`struct Snapshot;
// concave:invariant reads
// Preserve one snapshot for every read.
impl Read for Snapshot {
 // concave:invariant bounds
 // Bound each read's work.
 fn read() {}
}
`)
	resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, engine(t))
	doc, err := resolver.Document("owner.rs")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Invariants) != 2 || doc.Invariants[0].Declaration.Kind != "impl_item" {
		t.Fatal(doc.Invariants)
	}
	method := doc.Invariants[1]
	if applicable := doc.Applicable(method.Declaration.Range.Start.Line+1, 0); len(applicable) != 2 {
		t.Fatal("implementation obligation no longer applies to its method", applicable)
	}
	for _, symbol := range []string{"Snapshot", "Snapshot::read"} {
		if status, err := resolver.Witness("owner.rs", symbol); err != nil || status != "symbol_found" {
			t.Fatal(symbol, status, err)
		}
	}
}
func TestFakeDirectivesAndInterveningStatements(t *testing.T) {
	source := "const example = `// concave:invariant fake\n// prose`;\n/* // concave:invariant fake */\nconst regex = /concave:invariant/;\n"
	doc, err := contracts.Parse(engine(t), "owner.ts", []byte(source))
	if err != nil || len(doc.Invariants) != 0 {
		t.Fatal(doc, err)
	}
	source = "// concave:invariant x\n// obligation\nrun();\nfunction next() {}"
	if _, err = contracts.Parse(engine(t), "owner.ts", []byte(source)); err == nil {
		t.Fatal("jumped across execution")
	}
}
func TestReferenceAndWitnessResolution(t *testing.T) {
	source := []byte("// concave:invariant x\n// Must preserve atomicity.\nfn owner() {}\nmod tests { fn atomicity() {} }\n")
	resolver := contracts.New(func(path string) ([]byte, error) { return source, nil }, engine(t))
	if status, err := resolver.Witness("owner.rs", "owner"); err != nil || status != "symbol_found" {
		t.Fatal(status, err)
	}
	if _, err := resolver.Resolve("owner.rs", "#x"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("owner.rs", "#missing"); err == nil {
		t.Fatal("missing invariant accepted")
	}
	status, err := resolver.Witness("owner.rs", "tests::atomicity")
	if err != nil || status != "symbol_found" {
		t.Fatal(status, err)
	}
	status, err = resolver.Witness("owner.rs", "tests::absent")
	if err != nil || status != "symbol_not_resolved" {
		t.Fatal(status, err)
	}
	for _, ref := range []string{"../owner.rs#x", "owner.rs#", "owner.rs#x#y", "README.md#x"} {
		if _, _, err := contracts.Target("owner.rs", ref); err == nil {
			t.Fatal(ref)
		}
	}
}

func TestAttachmentIdentityDistinguishesTraitAndReceiver(t *testing.T) {
	for _, tc := range []struct{ path, before, after string }{
		{"owner.rs", "impl First for Owner {\n// concave:invariant x\n// Preserve the obligation.\nfn check() {}\n}", "impl Second for Owner {\n// concave:invariant x\n// Preserve the obligation.\nfn check() {}\n}"},
		{"owner.go", "package fixture\n// concave:invariant x\n// Preserve the obligation.\nfunc (r First) Check() {}", "package fixture\n// concave:invariant x\n// Preserve the obligation.\nfunc (r Second) Check() {}"},
	} {
		a, err := contracts.Parse(engine(t), tc.path, []byte(tc.before))
		if err != nil {
			t.Fatal(err)
		}
		b, err := contracts.Parse(engine(t), tc.path, []byte(tc.after))
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Invariants) != 1 || len(b.Invariants) != 1 || a.Invariants[0].Declaration.Identity == b.Invariants[0].Declaration.Identity {
			t.Fatalf("reattachment lost: %+v %+v", a, b)
		}
	}
}
