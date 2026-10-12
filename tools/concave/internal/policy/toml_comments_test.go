package policy_test

import (
	"reflect"
	"strings"
	"testing"

	"concave/internal/policy"
)

func TestTOMLAnnotationsIgnoreStrings(t *testing.T) {
	fake := strings.ReplaceAll(annotation("fake = true\n"), "//", "#")
	real := strings.ReplaceAll(annotation("real = true\n"), "//", "#")
	for name, prefix := range map[string]string{
		"basic":         "s = \"# concave:begin hidden \\\" quote\"\n",
		"literal":       "s = '# concave:begin hidden \\'\n",
		"quoted keys":   "\"# concave:begin hidden\" = '# concave:end hidden'\n",
		"multiline":     "s = \"\"\"\n" + fake + "\"\"\"\n",
		"multiliteral":  "s = '''\n" + fake + "'''\n",
		"escape triple": "s = \"\"\"\\\"\"\"\n" + fake + "\"\"\"\n",
		"line escape":   "s = \"\"\"\\\n" + fake + "\"\"\"\n",
		"backslashes":   "s = \"\"\"\\\\\"\"\"\n",
		"four quotes":   "s = \"\"\"\"text\"\"\"\"\n",
		"five quotes":   "s = '''''text'''''\n",
		"two quotes":    "s = \"\"\"one \"\"\n" + fake + "\"\"\"\n",
		"inline":        "value = true # concave:begin hidden\n",
		"comment quote": "# \"\"\" this comment does not open a string\n",
		"CRLF":          "s = '''\r\n" + strings.ReplaceAll(fake, "\n", "\r\n") + "'''\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			as, bad := policy.Annotations("Cargo.toml", prefix+real)
			a := as["read"]
			if len(bad) != 0 || len(as) != 1 || a.Code != "real = true\n" || a.Start != strings.Count(prefix, "\n") || a.Whole != real {
				t.Fatal(as, bad)
			}
		})
	}
	for _, quote := range []string{"\"", "'", "\"\"\"", "'''"} {
		as, bad := policy.Annotations("Cargo.toml", "s = "+quote+"\n"+fake)
		if len(as) != 0 || len(bad) != 0 {
			t.Fatal("unterminated string became an annotation", quote, as, bad)
		}
	}
}

func TestTOMLAnnotationMetadataAndRefusals(t *testing.T) {
	source := strings.ReplaceAll(annotation("testing = []\n"), "//", "#")
	source = strings.Replace(source, "# witness:", "#\n# witness:", 1)
	comments := policy.StandaloneComments("Cargo.toml", "\t # first\r\nvalue = 1 # inline\r\n# last")
	if !reflect.DeepEqual(comments, map[int]string{0: "first", 2: "last"}) {
		t.Fatal(comments)
	}
	for name, text := range map[string]string{
		"unknown":   strings.Replace(source, "# why:", "# ignore:", 1),
		"empty":     strings.Replace(source, "protect the current read", "", 1),
		"malformed": strings.Replace(source, "concave:begin read", "concave:begin", 1),
		"unclosed":  strings.Replace(source, "# concave:end read\n", "", 1),
		"duplicate": source + source,
		"nested":    strings.Replace(source, "testing = []", "# concave:begin nested\ntesting = []", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, bad := policy.Annotations("Cargo.toml", text); len(bad) == 0 {
				t.Fatal("invalid annotation accepted")
			}
		})
	}
	// TOML is not TypeScript: the parser refuses to decide, and never finds a fence.
	if as, _ := scripts(t).Annotations("x.ts", source); len(as) != 0 {
		t.Fatal("TOML markers parsed as TypeScript comments", as)
	}
	if as, bad := policy.Annotations("Cargo.toml", annotation("testing = []\n")); len(as) != 0 || len(bad) != 0 {
		t.Fatal("slash markers parsed as TOML comments", as, bad)
	}
}
