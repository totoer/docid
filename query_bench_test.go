package docid

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkQueryFilter(b *testing.B) {
	for _, n := range []int{100, 1000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("Docs=%d/Indexed=%t", n, indexed), func(b *testing.B) {
				db := benchmarkDB(b, false)
				c, _ := benchmarkUsers(b, db, n)
				if indexed {
					benchmarkCreateIndex(b, c, Index{Name: "score", Field: "score"})
					benchmarkCreateIndex(b, c, Index{Name: "city", Field: "profile.city"})
				}
				q := c.Find().As("u")
				for _, tc := range []struct {
					name string
					q    Query
					want int
				}{
					{"Equality1Percent", q.Where(F("u.score").Eq(42)), n / 100},
					{"EqualityMiss", q.Where(F("u.score").Eq(101)), 0},
					{"Range10Percent", q.Where(F("u.score").Gte(90)), n / 10},
					{"AndResidual", q.Where(And(F("u.score").Gte(90), F("u.active").Eq(true))), n / 20},
					{"Nested50Percent", q.Where(F("u.profile.city").Eq("Белград")), n / 2},
					{"OrFallback", q.Where(Or(F("u.score").Lt(10), F("u.score").Gte(90))), n / 5},
				} {
					b.Run(tc.name, func(b *testing.B) { benchmarkQuery(b, tc.q, tc.want) })
				}
			})
		}
	}
}

func BenchmarkQueryOrderPage(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			db := benchmarkDB(b, false)
			c, _ := benchmarkUsers(b, db, n)
			q := c.Find().As("u")
			ordered := q.OrderBy(Desc(F("u.score")), Asc(F("u.name")))
			page := ordered.Offset(20).Limit(10)
			rows, err := page.All(context.Background())
			// Scores repeat n/100 times, with name breaking ties by ID.
			wantFirst := benchmarkUserValue((99 - 20/(n/100)) + (20%(n/100))*100).ID
			if err != nil || len(rows) != 10 || rows[0]["u"]["id"] != wantFirst {
				b.Fatalf("incorrect ordered page: err=%v", err)
			}
			for _, tc := range []struct {
				name string
				q    Query
				want int
			}{
				{"Scan", q, n}, {"Sort", ordered, n},
				{"Page", q.Offset(20).Limit(10), 10}, {"SortPage", page, 10},
			} {
				b.Run(tc.name, func(b *testing.B) { benchmarkQuery(b, tc.q, tc.want) })
			}
		})
	}
}

func BenchmarkQueryJoin(b *testing.B) {
	// Three quarters of users have two orders; one quarter has no orders.
	// Smaller fixtures keep the intentionally quadratic fallback practical.
	for _, n := range []int{32, 128} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("Users=%d/Indexed=%t", n, indexed), func(b *testing.B) {
				db := benchmarkDB(b, false)
				users, values := benchmarkUsers(b, db, n)
				orders := benchmarkCollection(b, db, "orders")
				payments := benchmarkCollection(b, db, "payments")
				for i, u := range values {
					if i%4 == 0 {
						continue
					}
					for j := range 2 {
						id := fmt.Sprintf("o%06d-%d", i, j)
						if err := orders.Insert(id, Document{"id": id, "user_id": u.ID, "total": j + 1}); err != nil {
							b.Fatal(err)
						}
						if j == 1 {
							if err := payments.Insert(id, Document{"order_id": id, "paid": true}); err != nil {
								b.Fatal(err)
							}
						}
					}
				}
				if indexed {
					benchmarkCreateIndex(b, orders, Index{Name: "user", Field: "user_id"})
					benchmarkCreateIndex(b, payments, Index{Name: "order", Field: "order_id"})
				}
				q := users.Find().As("u")
				on := F("u.id").EqField(F("o.user_id"))
				inner := q.InnerJoin("orders", "o", on)
				for _, tc := range []struct {
					name string
					q    Query
					want int
				}{
					{"Inner", inner, n * 3 / 2},
					{"Left", q.LeftJoin("orders", "o", on), n * 7 / 4},
					{"CompoundOn", q.InnerJoin("orders", "o", And(on, F("o.total").Gt(1))), n * 3 / 4},
					{"WhereAfterJoin", inner.Where(F("o.total").Gt(1)), n * 3 / 4},
					{"Chain", inner.LeftJoin("payments", "p", F("o.id").EqField(F("p.order_id"))), n * 3 / 2},
				} {
					b.Run(tc.name, func(b *testing.B) { benchmarkQuery(b, tc.q, tc.want) })
				}
			})
		}
	}
}

func BenchmarkQueryDecode(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			db := benchmarkDB(b, false)
			c, _ := benchmarkUsers(b, db, n)
			q := c.Find().As("u").Where(F("u.active").Eq(true))
			benchmarkQueryDecode(b, q, n/2)
		})
	}
}

func BenchmarkQueryDecodeScan(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			db := benchmarkDB(b, false)
			c, _ := benchmarkUsers(b, db, n)
			benchmarkQueryDecode(b, c.Find().As("u"), n)
		})
	}
}

func benchmarkQueryDecode(b *testing.B, q Query, want int) {
	b.Helper()
	ctx := context.Background()
	b.Run("All", func(b *testing.B) { benchmarkQuery(b, q, want) })
	b.Run("SelectInto", func(b *testing.B) {
		var users []benchmarkUser
		b.ReportAllocs()
		for b.Loop() {
			if err := q.SelectInto(ctx, "u", &users); err != nil {
				b.Fatal(err)
			}
		}
		if len(users) != want || users[0].ID != benchmarkUserValue(0).ID || users[0].Profile.City != "Белград" {
			b.Fatal("incorrect typed projection")
		}
		b.ReportMetric(float64(want), "rows/op")
	})
	b.Run("AllInto", func(b *testing.B) {
		var rows []struct {
			User benchmarkUser `json:"u"`
		}
		b.ReportAllocs()
		for b.Loop() {
			if err := q.AllInto(ctx, &rows); err != nil {
				b.Fatal(err)
			}
		}
		if len(rows) != want || rows[0].User.ID != benchmarkUserValue(0).ID || rows[0].User.Profile.City != "Белград" {
			b.Fatal("incorrect typed rows")
		}
		b.ReportMetric(float64(want), "rows/op")
	})
}
