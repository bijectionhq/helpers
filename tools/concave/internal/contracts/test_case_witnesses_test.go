package contracts_test

import (
	"strings"
	"testing"

	"concave/internal/contracts"
)

// A Jest or Vitest test is a call, so a witness names its title: the full
// name the runner reports, or a trailing run of its titles, compared without
// whitespace because a fence reference holds none.
func TestJavaScriptTestTitlesResolveAsWitnessCases(t *testing.T) {
	source := []byte(`import { render } from "@testing-library/react";

const helper = () => "it('string fake', () => {})";
// it("comment fake", () => {});

describe("RotateCredentialDialog", () => {
  it("rotates the listed credential", async () => {
    render(<Dialog />);
  });
  test('keeps "both" names during the overlap', () => {});
  describe("when the list is empty", () => {
    it("renders nothing", () => {});
  });
  it.skip("skipped never runs", () => {});
  it.each([1, 2])("parameterized %s", () => {});
  it(` + "`has ${dynamic} title`" + `, () => {});
  it(` + "`static template`" + `, () => {});
  expect("not a test").toBe("not a test");
});

describe("TokenList", () => {
  it("renders nothing", () => {});
});

it("stands alone", () => {});
`)
	parser := engine(t)
	resolver := contracts.New(func(string) ([]byte, error) { return source, nil }, parser)
	for _, test := range []struct{ reference, resolution string }{
		{"RotateCredentialDialog rotates the listed credential", "test_case_found"},
		{"RotateCredentialDialogrotatesthelistedcredential", "test_case_found"},
		{"rotatesthelistedcredential", "test_case_found"},
		{`keeps"both"namesduringtheoverlap`, "test_case_found"},
		{"RotateCredentialDialogwhenthelistisemptyrendersnothing", "test_case_found"},
		{"whenthelistisemptyrendersnothing", "test_case_found"},
		{"TokenListrendersnothing", "test_case_found"},
		{"rendersnothing", "symbol_ambiguous"},
		{"standsalone", "test_case_found"},
		{"statictemplate", "test_case_found"},
		{"helper", "symbol_found"},
		{"RotateCredentialDialog", "symbol_not_resolved"},
		{"listedcredential", "symbol_not_resolved"},
		{"skippedneverruns", "symbol_not_resolved"},
		{"parameterized%s", "symbol_not_resolved"},
		{"stringfake", "symbol_not_resolved"},
		{"commentfake", "symbol_not_resolved"},
		{"notatest", "symbol_not_resolved"},
	} {
		t.Run(test.reference, func(t *testing.T) {
			resolution, err := resolver.Witness("Dialog.test.tsx", test.reference)
			if err != nil || resolution != test.resolution {
				t.Fatal(resolution, err)
			}
		})
	}
	if parser.Calls != 2 {
		t.Fatal("test titles must come from the ordinary parse batch", parser.Calls)
	}
	tree, err := parser.Parse("Dialog.test.tsx", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.TestCases) != 6 {
		t.Fatal(tree.TestCases)
	}
	for _, c := range tree.TestCases {
		call := string(source[c.Range.Bytes.Start:c.Range.Bytes.End])
		if !strings.HasPrefix(call, "it(") && !strings.HasPrefix(call, "test(") {
			t.Fatal("test case lost its source range", c)
		}
	}
}

// Plain JavaScript and TypeScript test files use the same discovery, and a
// file without tests has none.
func TestJavaScriptTestTitlesAcrossLanguages(t *testing.T) {
	for path, source := range map[string]string{
		"owner.test.ts":  "describe(\"values\", () => { it(\"round trips\", () => { const x: number = 1; }); });",
		"owner.test.mjs": "describe(\"values\", () => { it(\"round trips\", () => {}); });",
	} {
		resolver := contracts.New(func(string) ([]byte, error) { return []byte(source), nil }, engine(t))
		if resolution, err := resolver.Witness(path, "values round trips"); err != nil || resolution != "test_case_found" {
			t.Fatal(path, resolution, err)
		}
	}
	resolver := contracts.New(func(string) ([]byte, error) { return []byte("export const it = 1;\n"), nil }, engine(t))
	if resolution, err := resolver.Witness("owner.ts", "roundtrips"); err != nil || resolution != "symbol_not_resolved" {
		t.Fatal(resolution, err)
	}
}
