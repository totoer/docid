package docid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func createSearchIndex(t *testing.T, c *Collection, fields ...string) {
	t.Helper()
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: fields}); err != nil {
		t.Fatal(err)
	}
}
func textRowIDs(rows []Row, alias string) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r[alias] == nil {
			ids = append(ids, "nil")
		} else {
			ids = append(ids, r[alias]["id"].(string))
		}
	}
	return ids
}
func TestTextQueryValidation(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	createSearchIndex(t, c, "body")
	if err := c.CreateIndex(Index{Name: "scalar", Field: "body"}); err != nil {
		t.Fatal(err)
	}
	for _, expr := range []Expr{Match("docs", "missing", "a"), Match("docs", "scalar", "a"), Match("unknown", "search", "a"), Match("docs", "search", ""), Match("docs", "search", `"unterminated`), Match("docs", "search", "unknown:a"), Or(And(), Match("docs", "missing", "a")), And(Or(), Match("docs", "search", "NOT a"))} {
		for _, q := range []Query{c.Find().Where(expr), c.Find().Where(expr).Limit(0)} {
			if _, err := q.All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("invalid query returned %v", err)
			}
		}
	}
	q := c.Find().As("d").LeftJoin("docs", "other", Match("later", "search", "a")).Limit(0)
	if _, err := q.All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	q = c.Find().As("d").LeftJoin("missing", "other", Match("d", "search", "a"))
	if _, err := q.All(context.Background()); !errors.Is(err, ErrCollectionNotFound) {
		t.Fatal(err)
	}
}
func TestTextQueryCompositionAndCandidates(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	createSearchIndex(t, c, "body")
	insert(t, c, "a", Document{"id": "a", "body": "база данных", "age": 1})
	insert(t, c, "b", Document{"id": "b", "body": "другой текст", "age": 2})
	insert(t, c, "c", Document{"id": "c", "age": 3})
	m := Match("d", "search", "база")
	checks := []struct {
		expr Expr
		want []string
	}{{m, []string{"a"}}, {Or(m, F("d.age").Eq(2)), []string{"a", "b"}}, {Not(m), []string{"b", "c"}}, {And(m, F("d.age").Gt(1)), []string{}}, {Or(m, Not(F("d.body").Exists())), []string{"a", "c"}}, {And(Not(Not(m)), F("d.age").Lt(2)), []string{"a"}}}
	for _, indexed := range []bool{false, true} {
		if indexed {
			if err := c.CreateIndex(Index{Name: "age", Field: "age"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, check := range checks {
			q := c.Find().As("d").Where(check.expr)
			got := all(t, q)
			scan := all(t, c.Find().As("d").Where(Or(check.expr)))
			if !reflect.DeepEqual(got, scan) || !reflect.DeepEqual(textRowIDs(got, "d"), check.want) {
				t.Fatalf("indexed %t: got %v scan %v want %v", indexed, got, scan, check.want)
			}
		}
	}
	q := c.Find().As("d").Where(m)
	if len(all(t, q)) != 1 {
		t.Fatal("initial match")
	}
	if err := c.Put("a", Document{"id": "a", "body": "изменено"}); err != nil {
		t.Fatal(err)
	}
	if len(all(t, q)) != 0 {
		t.Fatal("stale Match set")
	}
	if err := c.DropTextIndex("search"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
	createSearchIndex(t, c, "body")
	if len(all(t, q)) != 0 {
		t.Fatal("stale rebuilt index")
	}
}
func TestTextQueryJoins(t *testing.T) {
	db := openTest(t)
	users := collection(t, db, "users")
	orders := collection(t, db, "orders")
	createSearchIndex(t, users, "bio")
	createSearchIndex(t, orders, "body")
	insert(t, users, "u1", Document{"id": "u1", "bio": "база"})
	insert(t, users, "u2", Document{"id": "u2", "bio": "другое", "parent_id": "u1"})
	insert(t, orders, "o1", Document{"id": "o1", "user_id": "u1", "body": "данные"})
	insert(t, orders, "o2", Document{"id": "o2", "user_id": "u1", "body": "другое"})
	on := F("u.id").EqField(F("o.user_id"))
	m := Match("o", "search", "данные")
	for _, indexed := range []bool{false, true} {
		if indexed {
			if err := orders.CreateIndex(Index{Name: "user", Field: "user_id"}); err != nil {
				t.Fatal(err)
			}
		}
		base := users.Find().As("u")
		q := base.LeftJoin("orders", "o", And(on, m))
		rows := all(t, q)
		if got := textRowIDs(rows, "o"); !reflect.DeepEqual(got, []string{"o1", "nil"}) {
			t.Fatal(indexed, got)
		}
		q = base.LeftJoin("orders", "o", on).Where(m)
		if got := textRowIDs(all(t, q), "o"); !reflect.DeepEqual(got, []string{"o1"}) {
			t.Fatal(got)
		}
		q = base.LeftJoin("orders", "o", on).Where(Not(m))
		if got := textRowIDs(all(t, q), "o"); !reflect.DeepEqual(got, []string{"o2", "nil"}) {
			t.Fatal(got)
		}
		q = base.InnerJoin("orders", "o", And(on, m)).Where(Match("u", "search", "база"))
		if len(all(t, q)) != 1 {
			t.Fatal("inner Match")
		}
		q = base.InnerJoin("users", "p", F("u.parent_id").EqField(F("p.id"))).Where(Match("p", "search", "база"))
		if got := textRowIDs(all(t, q), "u"); !reflect.DeepEqual(got, []string{"u2"}) {
			t.Fatal("self alias", got)
		}
	}
}
func TestTextQueryTransactions(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	c := collection(t, db, "docs")
	createSearchIndex(t, c, "body")
	orders := collection(t, db, "orders")
	createSearchIndex(t, orders, "body")
	insert(t, c, "a", Document{"id": "a", "body": "before"})
	insert(t, c, "b", Document{"id": "b", "body": "before"})
	var escaped Query
	err := db.Update(context.Background(), func(tx *Tx) error {
		tc := txCollection(t, tx, "docs")
		q := tc.Find().As("d").Where(Match("d", "search", "after"))
		escaped = q
		if len(all(t, q)) != 0 {
			t.Fatal("unexpected before")
		}
		if err := tc.Put("a", Document{"id": "a", "body": "after token"}); err != nil {
			return err
		}
		if err := tc.Insert("c", Document{"id": "c", "body": "after token"}); err != nil {
			return err
		}
		if err := tc.Delete("b"); err != nil {
			return err
		}
		if got := textRowIDs(all(t, q), "d"); !reflect.DeepEqual(got, []string{"a", "c"}) {
			t.Fatal(got)
		}
		if err := tc.Insert("a", Document{"id": "a", "body": "rejected"}); !errors.Is(err, ErrAlreadyExists) {
			t.Fatal(err)
		}
		if len(all(t, tx.From("docs").Where(Match("docs", "search", "rejected")))) != 0 {
			t.Fatal("rejected text")
		}
		o := txCollection(t, tx, "orders")
		if err := o.Insert("o", Document{"id": "o", "doc_id": "a", "body": "after"}); err != nil {
			return err
		}
		joined := tx.From("docs").As("d").InnerJoin("orders", "o", And(F("d.id").EqField(F("o.doc_id")), Match("o", "search", "after"))).Where(Match("d", "search", `"after token"`))
		if len(all(t, joined)) != 1 {
			t.Fatal("staged text join")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.All(context.Background()); !errors.Is(err, ErrTxClosed) {
		t.Fatal(err)
	}
	db = reopenTestDB(t, db, path)
	if len(all(t, db.From("docs").Where(Match("docs", "search", `"after token"`)))) != 2 {
		t.Fatal("text commit lost")
	}
	reject := errors.New("rollback")
	err = db.Update(context.Background(), func(tx *Tx) error {
		tc := txCollection(t, tx, "docs")
		if err := tc.Put("a", Document{"id": "a", "body": "rolledback"}); err != nil {
			return err
		}
		if len(all(t, tc.Find().Where(Match("docs", "search", "rolledback")))) != 1 {
			t.Fatal("missing own write")
		}
		return reject
	})
	if !errors.Is(err, reject) || len(all(t, db.From("docs").Where(Match("docs", "search", "rolledback")))) != 0 {
		t.Fatal(err)
	}
}
func TestTextQueryTypedPaginationAndRaw(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	createSearchIndex(t, c, "body")
	for i := 0; i < 8; i++ {
		insert(t, c, fmt.Sprint(i), json.RawMessage(fmt.Sprintf(`{ "id": "%d", "body": "база данных", "n": %d, "extra":1.0 }`, i, i)))
	}
	q := c.Find().As("d").Where(Match("d", "search", "база")).OrderBy(Desc(F("d.n"))).Offset(2).Limit(3)
	want := all(t, q)
	var rows []Row
	if err := q.AllInto(context.Background(), &rows); err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatal(rows, err)
	}
	var docs []Document
	if err := q.SelectInto(context.Background(), "d", &docs); err != nil {
		t.Fatal(err)
	}
	for i := range docs {
		if !reflect.DeepEqual(docs[i], want[i]["d"]) {
			t.Fatal(docs, want)
		}
	}
	defer func() { queryDecodeHook = nil }()
	queryDecodeHook = func() error { return c.Put("hook", Document{"body": "after snapshot"}) }
	var hooked hookedDocuments
	if err := q.SelectInto(context.Background(), "d", &hooked); err != nil {
		t.Fatal(err)
	}
	queryDecodeHook = nil
	var raw []txRawJSON
	if err := q.SelectInto(context.Background(), "d", &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || raw[0].Raw != `{"id":"5","body":"база данных","n":5,"extra":1.0}` {
		t.Fatal(raw)
	}
	destination := []struct{ N bool }{{N: true}}
	before := append([]struct{ N bool }(nil), destination...)
	if err := q.SelectInto(context.Background(), "d", &destination); err == nil || !reflect.DeepEqual(destination, before) {
		t.Fatal("decode failure mutated destination", err)
	}
}
func TestTextQueryContexts(t *testing.T) {
	for _, source := range []string{"ordinary", "query", "tx"} {
		t.Run(source, func(t *testing.T) {
			db := openTest(t)
			c := collection(t, db, "docs")
			createSearchIndex(t, c, "body")
			for i := 0; i < 30; i++ {
				insert(t, c, fmt.Sprint(i), Document{"body": "база данных"})
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			stepped := &cancelOnCheckContext{Context: base, cancel: cancel}
			check := func(q Query, ctx context.Context) {
				stepped.checks, stepped.at = 0, 12
				dst := []Document{{"marker": "unchanged"}}
				err := q.SelectInto(ctx, "docs", &dst)
				if !errors.Is(err, context.Canceled) || len(dst) != 1 || dst[0]["marker"] != "unchanged" {
					t.Fatal(dst, err)
				}
			}
			if source == "ordinary" {
				check(c.Find().Where(Match("docs", "search", "баз*")), stepped)
				return
			}
			txCtx, queryCtx := context.Context(context.Background()), context.Context(stepped)
			if source == "tx" {
				txCtx, queryCtx = stepped, context.Background()
			}
			err := db.Update(txCtx, func(tx *Tx) error {
				tc := txCollection(t, tx, "docs")
				check(tc.Find().Where(Match("docs", "search", "NEAR(база данных, 2)")), queryCtx)
				if source == "query" {
					return tc.Put("handled", Document{"body": "продолжение"})
				}
				return nil
			})
			if source == "tx" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTextQueryConcurrentBuilder(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	createSearchIndex(t, c, "body")
	insert(t, c, "a", Document{"body": "база данных"})
	q := c.Find().Where(Match("docs", "search", "база"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := c.Put("a", Document{"body": "база данных"}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rows, err := q.All(context.Background())
				if err != nil || len(rows) != 1 {
					t.Errorf("shared builder: %v %v", rows, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
func TestTextQueryFixtures(t *testing.T) {
	cases, results := loadTextFixtures(t)
	for i, c := range cases {
		if results[i].InvalidDefinition {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			path := t.TempDir()
			db := reopenTestDB(t, nil, path)
			collection := collection(t, db, "docs")
			for _, d := range c.Documents {
				doc := textFixtureDoc(c, d)
				doc["id"] = d.ID
				insert(t, collection, d.ID, doc)
			}
			if err := collection.CreateTextIndex(TextIndex{Name: "search", Fields: c.Fields, Tokenizer: c.Tokenizer}); err != nil {
				t.Fatal(err)
			}
			for pass := 0; pass < 2; pass++ {
				for _, q := range results[i].Queries {
					rows, err := db.From("docs").As("d").Where(Match("d", "search", q.Query)).All(context.Background())
					if (err != nil) != q.Error {
						t.Errorf("pass %d query %q: %v", pass, shortText(q.Query), err)
						continue
					}
					if err == nil {
						ids := textRowIDs(rows, "d")
						sort.Strings(ids)
						if !reflect.DeepEqual(ids, q.IDs) {
							t.Errorf("pass %d query %q: got %v want %v", pass, shortText(q.Query), ids, q.IDs)
						}
					}
				}
				if pass == 0 {
					for _, d := range c.Documents {
						if err := collection.Put(d.ID, Document{"id": d.ID, "body": "temporary"}); err != nil {
							t.Fatal(err)
						}
						if err := collection.Delete(d.ID); err != nil {
							t.Fatal(err)
						}
						doc := textFixtureDoc(c, d)
						doc["id"] = d.ID
						if err := collection.Insert(d.ID, doc); err != nil {
							t.Fatal(err)
						}
					}
					db = reopenTestDB(t, db, path)
				}
			}
		})
	}
}
