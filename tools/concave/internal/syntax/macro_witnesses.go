package syntax

// proptestWitnessRule locates the function-shaped inputs documented by proptest:
// https://docs.rs/proptest/latest/proptest/macro.proptest.html
// Matching immediate token siblings excludes functions in strings, nested bodies,
// configuration expressions and closure-style invocations. Discovery stays in the
// ordinary parse batch. These inputs cannot receive declaration invariants.
func proptestWitnessRule() map[string]any {
	return NativeRule("witness-proptest", "Rust", map[string]any{
		"kind": "identifier", "pattern": "$NAME",
		"inside": map[string]any{
			"kind": "token_tree",
			"inside": map[string]any{
				"kind": "macro_invocation",
				"has":  map[string]any{"field": "macro", "regex": `^(::)?(proptest::)?proptest$`},
			},
		},
		"follows": map[string]any{"regex": "^fn$", "pattern": "$$FN"},
		"precedes": map[string]any{
			"kind": "token_tree", "regex": `^\(`,
			"precedes": map[string]any{"kind": "token_tree", "regex": `^\{`, "pattern": "$BODY"},
		},
	})
}
