package docid

import "testing"

func TestTextParserFixtures(t *testing.T) {
	cases, results := loadTextFixtures(t)
	for i, c := range cases {
		if results[i].InvalidDefinition {
			continue
		}
		for _, q := range results[i].Queries {
			n, err := parseTextQuery(q.Query)
			if err == nil {
				err = n.validateFields(c.Fields)
			}
			if (err != nil) != q.Error {
				t.Errorf("%s %q: syntax/binding error %v, want error=%t", c.Name, shortText(q.Query), err, q.Error)
			}
		}
	}
	for _, s := range []string{"NOT a", "(a OR b) c", "a (b)", "NEAR()", "NEAR(^a b)", "a AND", "\"unterminated", "a**", "{}:a", "near(a b)"} {
		if _, err := parseTextQuery(s); err == nil {
			t.Errorf("accepted invalid %q", s)
		}
	}
}
func shortText(s string) string {
	if len(s) > 100 {
		return s[:100] + "..."
	}
	return s
}
func TestTextParserPrecedence(t *testing.T) {
	n, err := parseTextQuery("a OR b NOT c d AND e")
	if err != nil || n.kind != "OR" || n.right.kind != "AND" || n.right.left.kind != "NOT" || n.right.left.right.kind != "implicit" {
		t.Fatalf("incorrect precedence: %#v %v", n, err)
	}
}
func FuzzTextParser(f *testing.F) {
	for _, s := range []string{"a NOT b c", `title:"база данных"`, "NEAR(a b, 3)", "", `"" + a`, "a\x00b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = parseTextQuery(s) })
}
