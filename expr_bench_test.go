package docid

import (
	"encoding/json"
	"testing"
)

func BenchmarkExprEval(b *testing.B) {
	// Alternate matches and misses, and include missing fields. Constructing and
	// validating the DSL is deliberately outside evaluation timing.
	const n = 100
	rows := make([]Row, n)
	for i := range rows {
		_, doc, err := encodeDocument(benchmarkUserValue(i))
		if err != nil {
			b.Fatal(err)
		}
		doc["large"] = json.Number("9007199254740993")
		if i%2 == 0 {
			doc["optional"] = true
		}
		rows[i] = Row{"u": doc, "o": {"user_id": doc["id"]}}
	}
	for _, tc := range []struct {
		name string
		expr Expr
		want int
	}{
		{"NumericRange", And(F("u.score").Gte(25), F("u.score").Lt(75)), 50},
		{"NestedField", F("u.profile.city").Eq("Белград"), 50},
		{"AndOrNot", And(Or(F("u.score").Lt(25), F("u.score").Gte(75)), Not(F("u.active").Eq(false))), 25},
		{"In", F("u.score").In(1, 25, 50, 75, 99), 5},
		{"MissingField", Not(F("u.optional").Exists()), 50},
		{"JoinEquality", F("u.id").EqField(F("o.user_id")), n},
		{"ExactNumber", F("u.large").Gt(json.Number("9007199254740992")), n},
	} {
		b.Run(tc.name, func(b *testing.B) {
			if err := tc.expr.validate(map[string]string{"u": "users", "o": "orders"}); err != nil {
				b.Fatal(err)
			}
			eval := func() int {
				matches := 0
				for _, row := range rows {
					if tc.expr.eval(row) {
						matches++
					}
				}
				return matches
			}
			if got := eval(); got != tc.want {
				b.Fatalf("matches=%d, want=%d", got, tc.want)
			}
			b.ReportAllocs()
			var got int
			for b.Loop() {
				got = eval()
			}
			if got != tc.want {
				b.Fatal("evaluation result changed")
			}
			b.ReportMetric(n, "docs/op")
		})
	}
}
