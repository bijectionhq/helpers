package contracts_test

import (
	"strings"
	"testing"

	"concave/internal/contracts"
)

func TestProptestWitnessInputsAreScopedAndDistinctFromDeclarations(t *testing.T) {
	source := []byte(`mod outer {
mod tests {
    proptest! {
        #![proptest_config({ fn config_helper() {} Config::default() })]
        // fn comment_fake(x in any::<u8>()) {}
        #[test]
        fn roundtrip(
            input in { fn strategy_helper() {} any::<String>() }
        ) {
            fn body_helper() {}
            let text = "fn string_fake(x in any::<u8>()) {}";
            prop_assert_eq!(input, input);
        }
    }
    ::proptest::proptest! {
        #[test]
        fn unicode_case(input: String) { let label = "été"; }
    }
    fn ordinary() {}
    unrelated! { fn opaque(input: u8) {} }
    alias! { fn aliased(input: u8) {} }
    proptest!(|(x in 0u8..10)| { fn closure_helper() {} });
}
}`)
	parser := engine(t)
	resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, parser)
	for _, test := range []struct{ reference, resolution string }{
		{"outer::tests::roundtrip", "macro_input_found"},
		{"tests::unicode_case", "macro_input_found"},
		{"roundtrip", "macro_input_found"},
		{"tests::ordinary", "symbol_found"},
		{"other::roundtrip", "symbol_not_resolved"},
		{"tests::absent", "symbol_not_resolved"},
		{"config_helper", "symbol_not_resolved"},
		{"comment_fake", "symbol_not_resolved"},
		{"strategy_helper", "symbol_not_resolved"},
		{"body_helper", "symbol_not_resolved"},
		{"string_fake", "symbol_not_resolved"},
		{"opaque", "symbol_not_resolved"},
		{"aliased", "symbol_not_resolved"},
		{"closure_helper", "symbol_not_resolved"},
	} {
		t.Run(test.reference, func(t *testing.T) {
			resolution, err := resolver.Witness("owner.rs", test.reference)
			if err != nil || resolution != test.resolution {
				t.Fatal(resolution, err)
			}
		})
	}
	if parser.Calls != 2 {
		t.Fatal("witness discovery repeated ast-grep instead of sharing the parse batch", parser.Calls)
	}
	tree, err := parser.Parse("owner.rs", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.MacroWitnesses) != 2 {
		t.Fatal(tree.MacroWitnesses)
	}
	for _, input := range tree.MacroWitnesses {
		body := string(source[input.Range.Bytes.Start:input.Range.Bytes.End])
		if !strings.HasPrefix(body, "fn "+input.Name+"(") || !strings.HasSuffix(body, "}") || input.Range.Start.Line < 1 {
			t.Fatal("macro input lost its source range", input)
		}
		for _, declaration := range tree.Declarations {
			if input.Name == declaration.Name {
				t.Fatal("macro input became an ordinary declaration", declaration)
			}
		}
	}
}

func TestRustMacroDefinitionWitnesses(t *testing.T) {
	source := []byte(`mod serialization {
 // concave:invariant roundtrip
 // Generated conversions preserve every representable value.
 #[macro_export]
 macro_rules! codegen {
  ($input:ident) => { fn generated() {} macro_rules! emitted { () => {} } };
 }
 const EXAMPLE: &str = "macro_rules! string_fake { () => {} }";
 // macro_rules! comment_fake { () => {} }
}
mod first { macro_rules! duplicate { () => {} } }
mod second { macro_rules! duplicate { () => {} } }
`)
	parser := engine(t)
	resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, parser)
	doc, err := resolver.Document("owner.rs")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Invariants) != 1 || doc.Invariants[0].Declaration.Kind != "macro_definition" {
		t.Fatal(doc.Invariants)
	}
	declaration := doc.Invariants[0].Declaration
	if declaration.Name != "codegen" || !strings.HasPrefix(string(source[declaration.Range.Bytes.Start:declaration.Range.Bytes.End]), "macro_rules! codegen") {
		t.Fatal("macro definition lost its name or source range", declaration)
	}
	for _, test := range []struct{ symbol, resolution string }{
		{"serialization::codegen", "symbol_found"},
		{"codegen", "symbol_found"},
		{"generated", "symbol_not_resolved"},
		{"emitted", "symbol_not_resolved"},
		{"string_fake", "symbol_not_resolved"},
		{"comment_fake", "symbol_not_resolved"},
		{"duplicate", "symbol_ambiguous"},
		{"first::duplicate", "symbol_found"},
	} {
		t.Run(test.symbol, func(t *testing.T) {
			status, err := resolver.Witness("owner.rs", test.symbol)
			if err != nil || status != test.resolution {
				t.Fatal(status, err)
			}
		})
	}
	if parser.Calls != 2 {
		t.Fatal("macro definitions must use the ordinary parse batch", parser.Calls)
	}
}

func TestProptestWitnessAmbiguityIncludesOrdinaryDeclarations(t *testing.T) {
	for _, duplicate := range []string{
		"fn roundtrip() {}",
		"proptest::proptest! { fn roundtrip(input: u8) {} }",
	} {
		source := []byte("mod tests { proptest! { fn roundtrip(input in any::<u8>()) {} } " + duplicate + " }")
		resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, engine(t))
		if resolution, err := resolver.Witness("owner.rs", "tests::roundtrip"); err != nil || resolution != "symbol_ambiguous" {
			t.Fatal(resolution, err)
		}
	}
}

func TestProptestInputsDoNotAcceptDeclarationInvariants(t *testing.T) {
	source := []byte("mod tests { proptest! {\n// concave:invariant roundtrip\n// Preserve the value.\nfn roundtrip(input: u8) {}\n} }")
	if _, err := contracts.Parse(engine(t), "owner.rs", source); err == nil || !strings.Contains(err.Error(), "immediate declaration") {
		t.Fatal("macro input was treated as an expanded declaration", err)
	}
}

func TestMalformedProptestInputsRemainUnresolved(t *testing.T) {
	for _, input := range []string{
		"fn missing_body(input: u8)",
		"fn missing_parameters {}",
		"fn unsupported_return(input: u8) -> bool { true }",
	} {
		name := strings.Fields(input)[1]
		name, _, _ = strings.Cut(name, "(")
		source := []byte("proptest! { " + input + " }")
		resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, engine(t))
		if resolution, err := resolver.Witness("owner.rs", name); err != nil || resolution != "symbol_not_resolved" {
			t.Fatal(resolution, err)
		}
	}
}
