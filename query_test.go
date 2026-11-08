package docid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestFiltersAndIndexEquivalence(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	values := []any{nil, false, true, -10, -1.25, 0, 1, json.Number("1.00"), 2, 100, "", "1", "z", []int{1}, Document{"n": 1}, json.Number("9007199254740993"), json.Number("1e1000000000"), json.Number("-1e-1000000000")}
	insert(t, c, "missing", Document{"marker": "missing"})
	for i, v := range values {
		insert(t, c, fmt.Sprintf("%03d", i), Document{"v": v, "nested": Document{"v": v}})
	}
	f := F("docs.v")
	exprs := []Expr{
		f.Exists(), Not(f.Exists()), f.IsNull(), f.In(), f.In(nil, 1, "z"),
		And(f.Gte(-1), f.Lte(2)), Or(f.Eq(nil), f.Eq("z")), Not(f.Eq(1)),
		And(Or(f.Eq(1), f.Eq(2)), f.Gt(0)),
		F("docs.nested.v").EqField(f), And(), Or(),
	}
	for _, value := range values[:13] {
		exprs = append(exprs, f.Eq(value), f.Ne(value), f.Gt(value), f.Gte(value), f.Lt(value), f.Lte(value))
	}
	exprs = append(exprs, f.Eq(json.Number("9007199254740993")), f.Gt(json.Number("1e999999999")))
	before := make([][]Row, len(exprs))
	for i, expr := range exprs {
		before[i] = all(t, c.Find().Where(expr))
	}
	if len(all(t, c.Find().Where(f.Eq(1)))) != 2 {
		t.Fatal("numeric equality")
	}
	if len(all(t, c.Find().Where(f.IsNull()))) != 1 {
		t.Fatal("null/missing conflated")
	}
	if len(all(t, c.Find().Where(Not(f.Exists())))) != 1 {
		t.Fatal("exists")
	}
	for _, field := range []string{"v", "nested.v"} {
		if err := c.CreateIndex(Index{Name: field, Field: field}); err != nil {
			t.Fatal(err)
		}
	}
	for i, expr := range exprs {
		got := all(t, c.Find().Where(expr))
		if !reflect.DeepEqual(got, before[i]) {
			t.Fatalf("expression %d: indexed %v vs scan %v", i, got, before[i])
		}
	}
}

func TestJoinVariants(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprintf("indexed=%t", indexed), func(t *testing.T) {
			db := openTest(t)
			users, orders, payments := collection(t, db, "users"), collection(t, db, "orders"), collection(t, db, "payments")
			insert(t, users, "1", Document{"id": 1, "name": "Анна"})
			insert(t, users, "2", Document{"id": 2, "name": "Борис"})
			insert(t, users, "3", Document{"name": "Missing"})
			insert(t, users, "4", Document{"id": nil, "name": "Null"})
			insert(t, orders, "1", Document{"id": "a", "user_id": 1, "total": 20})
			insert(t, orders, "2", Document{"id": "b", "user_id": json.Number("1.0"), "total": 50})
			insert(t, orders, "3", Document{"id": "c", "user_id": nil, "total": 10})
			insert(t, orders, "4", Document{"id": "d", "total": 99})
			insert(t, payments, "1", Document{"order_id": "b", "paid": true})
			if indexed {
				if err := orders.CreateIndex(Index{Name: "user", Field: "user_id"}); err != nil {
					t.Fatal(err)
				}
				if err := payments.CreateIndex(Index{Name: "order", Field: "order_id"}); err != nil {
					t.Fatal(err)
				}
			}
			on := F("u.id").EqField(F("o.user_id"))
			q := db.From("users").As("u")
			inner := all(t, q.InnerJoin("orders", "o", on))
			if len(inner) != 3 || inner[0]["o"]["id"] != "a" || inner[1]["o"]["id"] != "b" || inner[2]["u"]["name"] != "Null" {
				t.Fatal(inner)
			}
			left := all(t, q.LeftJoin("orders", "o", on))
			if len(left) != 5 || left[2]["o"] != nil || left[3]["o"] != nil {
				t.Fatal(left)
			}
			filtered := all(t, q.LeftJoin("orders", "o", on).Where(F("o.total").Gt(25)))
			if len(filtered) != 1 || filtered[0]["o"]["id"] != "b" {
				t.Fatal(filtered)
			}
			missing := all(t, q.LeftJoin("orders", "o", on).Where(Not(F("o.id").Exists())))
			if len(missing) != 2 {
				t.Fatal(missing)
			}
			compound := all(t, q.LeftJoin("orders", "o", And(on, F("o.total").Gt(25))))
			if len(compound) != 4 || compound[0]["o"]["id"] != "b" || compound[3]["o"] != nil {
				t.Fatal(compound)
			}
			chain := all(t, q.InnerJoin("orders", "o", F("o.user_id").EqField(F("u.id"))).InnerJoin("payments", "p", F("o.id").EqField(F("p.order_id"))).Where(F("p.paid").Eq(true)))
			if len(chain) != 1 || chain[0]["o"]["id"] != "b" {
				t.Fatal(chain)
			}
			// Joining the same collection under another alias also works.
			self := all(t, q.InnerJoin("users", "v", F("u.id").EqField(F("v.id"))))
			if len(self) != 3 {
				t.Fatal(self)
			}
			// A non-equijoin ON predicate falls back to scanning.
			cross := all(t, q.InnerJoin("orders", "o", And(F("u.id").Eq(2), F("o.total").Gt(25))))
			if len(cross) != 2 {
				t.Fatal(cross)
			}
		})
	}
}

func TestSortPaginationAndReusableBuilder(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "c")
	for _, item := range []struct {
		id    string
		value any
	}{{"z", 10}, {"a", 2}, {"b", 2}, {"null", nil}, {"str", "x"}} {
		insert(t, c, item.id, Document{"id": item.id, "n": item.value})
	}
	insert(t, c, "missing", Document{"id": "missing"})
	q := c.Find()
	ordered := all(t, q.OrderBy(Asc(F("c.n")), Desc(F("c.id"))))
	want := []string{"missing", "null", "b", "a", "z", "str"}
	for i, row := range ordered {
		if row["c"]["id"] != want[i] {
			t.Fatal(ordered)
		}
	}
	page := all(t, q.OrderBy(Asc(F("c.n")), Desc(F("c.id"))).Offset(2).Limit(2))
	if len(page) != 2 || page[0]["c"]["id"] != "b" || page[1]["c"]["id"] != "a" {
		t.Fatal(page)
	}
	if len(all(t, q.Limit(0))) != 0 || len(all(t, q.Offset(100))) != 0 {
		t.Fatal("pagination")
	}
	if len(all(t, q.Where(F("c.n").Eq(2)))) != 2 {
		t.Fatal("where")
	}
	if len(all(t, q)) != 6 {
		t.Fatal("builder mutated")
	}
	base := q.Where(F("c.id").Exists()).OrderBy(Asc(F("c.n")))
	branch1 := base.Where(F("c.n").Eq(2)).OrderBy(Desc(F("c.id")))
	branch2 := base.Where(F("c.n").Eq(10)).OrderBy(Asc(F("c.id")))
	if len(all(t, branch1)) != 2 || len(all(t, branch2)) != 1 || len(all(t, base)) != 6 {
		t.Fatal("branching mutated query")
	}
}

func TestQueryValidationAndCancellation(t *testing.T) {
	db := openTest(t)
	collection(t, db, "c")
	collection(t, db, "other")
	queries := []Query{
		db.From("c").Where(F("c.n").EqField(F(""))),
		db.From("c").As(""), db.From("c").As("c.x"), db.From("c").Where(Expr{}),
		db.From("c").Where(F("x.n").Eq(1)), db.From("c").Where(F("c.").Eq(1)),
		db.From("c").Where(F("c.n").Eq([]int{1})), db.From("c").Limit(-1), db.From("c").Offset(-1),
		db.From("c").OrderBy(Asc(F("unknown.n"))),
		db.From("c").InnerJoin("other", "c", F("c.n").Eq(1)),
		db.From("c").InnerJoin("other", "o", F("future.n").EqField(F("o.n"))),
	}
	for i, q := range queries {
		if _, err := q.All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	for _, q := range []Query{db.From("missing"), db.From("c").InnerJoin("missing", "m", And())} {
		if _, err := q.All(context.Background()); !errors.Is(err, ErrCollectionNotFound) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.From("c").All(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := db.From("c").All(nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	if _, err := (Query{}).All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}
