package docid

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestTxQueryValidationAndLifetime(t *testing.T) {
	db := openTest(t)
	for _, outcome := range []string{"commit", "rollback", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			var escaped []Query
			reject := errors.New("reject")
			func() {
				if outcome == "panic" {
					defer func() {
						if p := recover(); p != reject {
							t.Fatalf("panic=%v", p)
						}
					}()
				}
				err := db.Update(context.Background(), func(tx *Tx) error {
					c := txCollection(t, tx, "docs")
					if err := c.Put("x", Document{"n": 1}); err != nil {
						return err
					}
					escaped = []Query{tx.From("docs"), c.Find()}
					if _, err := tx.From("missing").All(context.Background()); !errors.Is(err, ErrCollectionNotFound) {
						t.Fatal(err)
					}
					if _, err := tx.From("docs").InnerJoin("missing", "m", F("docs.n").EqField(F("m.n"))).All(context.Background()); !errors.Is(err, ErrCollectionNotFound) {
						t.Fatal(err)
					}
					for _, q := range []Query{c.Find().As("bad.alias"), c.Find().Where(F("unknown.n").Eq(1)), c.Find().Limit(-1), c.Find().Offset(-1)} {
						if _, err := q.All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
							t.Fatal(err)
						}
					}
					if _, err := c.Find().All(nil); !errors.Is(err, ErrInvalidQuery) {
						t.Fatal(err)
					}
					if err := c.Find().SelectInto(context.Background(), "unknown", new([]Document)); !errors.Is(err, ErrInvalidQuery) {
						t.Fatal(err)
					}
					if outcome == "panic" {
						panic(reject)
					}
					if outcome == "rollback" {
						return reject
					}
					return nil
				})
				if outcome == "rollback" {
					if !errors.Is(err, reject) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}()
			var zero Tx
			escaped = append(escaped, zero.From("docs"), (*Tx)(nil).From("docs"))
			for _, q := range escaped {
				if _, err := q.All(context.Background()); !errors.Is(err, ErrTxClosed) {
					t.Fatal(err)
				}
				if err := q.AllInto(context.Background(), nil); !errors.Is(err, ErrTxClosed) {
					t.Fatal(err)
				}
				if err := q.SelectInto(context.Background(), "docs", nil); !errors.Is(err, ErrTxClosed) {
					t.Fatal(err)
				}
			}
			if _, err := db.From("missing").All(context.Background()); !errors.Is(err, ErrCollectionNotFound) {
				t.Fatal(err)
			}
		})
	}
}

// Cancels on an observable operation check, not a wall-clock delay.
type cancelOnCheckContext struct {
	context.Context
	cancel     context.CancelFunc
	checks, at int
}

func (ctx *cancelOnCheckContext) Err() error {
	ctx.checks++
	if ctx.at > 0 && ctx.checks == ctx.at {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestTxQueryContextsDuringScan(t *testing.T) {
	for _, source := range []string{"tx", "query"} {
		t.Run(source, func(t *testing.T) {
			db := openTest(t)
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			stepped := &cancelOnCheckContext{Context: base, cancel: cancel}
			var txCtx, queryCtx context.Context = context.Background(), context.Background()
			if source == "tx" {
				txCtx = stepped
			} else {
				queryCtx = stepped
			}
			err := db.Update(txCtx, func(tx *Tx) error {
				c := txCollection(t, tx, "docs")
				for i := 0; i < 8; i++ {
					if err := c.Put(fmt.Sprint(i), Document{"n": i}); err != nil {
						return err
					}
				}
				stepped.checks, stepped.at = 0, 5
				if _, err := c.Find().All(queryCtx); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if stepped.checks < 5 {
					t.Fatal("query did not check cancellation during execution")
				}
				if source == "query" {
					return c.Put("handled", Document{"n": 9})
				}
				if _, err := c.Get("0"); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if err := c.Put("late", Document{}); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return nil
			})
			if source == "tx" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if names, err := db.Collections(); err != nil || len(names) != 0 {
					t.Fatalf("%v: %v", names, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got := all(t, db.From("docs")); len(got) != 9 {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestTxQueryIndexesAndJoins(t *testing.T) {
	db := openTest(t)
	for _, suffix := range []string{"_i", "_s"} {
		u := collection(t, db, "users"+suffix)
		o := collection(t, db, "orders"+suffix)
		p := collection(t, db, "payments"+suffix)
		for _, d := range []struct {
			id  string
			doc Document
		}{
			{"a", Document{"id": "a", "score": 1, "parent": "c"}},
			{"b", Document{"id": "b", "score": 2}},
			{"c", Document{"id": "c", "score": 3}},
		} {
			insert(t, u, d.id, d.doc)
		}
		for _, d := range []struct {
			id    string
			user  string
			total int
		}{{"o1", "a", 10}, {"o2", "b", 20}, {"o3", "c", 30}} {
			insert(t, o, d.id, Document{"id": d.id, "user_id": d.user, "total": d.total})
		}
		insert(t, p, "p1", Document{"order_id": "o1", "status": "paid"})
		if suffix == "_i" {
			for _, def := range []Index{{Name: "score", Field: "score"}, {Name: "id", Field: "id", Unique: true}} {
				if err := u.CreateIndex(def); err != nil {
					t.Fatal(err)
				}
			}
			if err := o.CreateIndex(Index{Name: "user", Field: "user_id"}); err != nil {
				t.Fatal(err)
			}
			if err := p.CreateIndex(Index{Name: "order", Field: "order_id"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	err := db.Update(context.Background(), func(tx *Tx) error {
		before := tx.From("users_i").As("u")
		if got := all(t, before); len(got) != 3 {
			t.Fatal(got)
		}
		for _, suffix := range []string{"_i", "_s"} {
			u := txCollection(t, tx, "users"+suffix)
			o := txCollection(t, tx, "orders"+suffix)
			p := txCollection(t, tx, "payments"+suffix)
			for _, d := range []struct {
				id  string
				doc Document
			}{
				{"a", Document{"id": "a", "score": 5, "parent": "c"}},
				{"d", Document{"id": "d", "score": 4, "parent": "a"}},
				{"e", Document{"id": "e", "score": 0, "parent": "a"}},
			} {
				if err := u.Put(d.id, d.doc); err != nil {
					return err
				}
			}
			if err := u.Delete("b"); err != nil {
				return err
			}
			if err := o.Put("o1", Document{"id": "o1", "user_id": "d", "total": 99}); err != nil {
				return err
			}
			if err := o.Delete("o2"); err != nil {
				return err
			}
			for _, d := range []struct {
				id    string
				total int
			}{{"o4", 50}, {"o5", 60}} {
				if err := o.Insert(d.id, Document{"id": d.id, "user_id": "a", "total": d.total}); err != nil {
					return err
				}
			}
			if err := p.Put("p1", Document{"order_id": "o4", "status": "paid"}); err != nil {
				return err
			}
			if err := p.Insert("p2", Document{"order_id": "o5", "status": "paid"}); err != nil {
				return err
			}
		}
		if got := all(t, before); len(got) != 4 {
			t.Fatal(got)
		}
		for _, predicate := range []Expr{
			F("u.score").Eq(4), F("u.score").Gt(3), F("u.score").Gte(4), F("u.score").Lt(4), F("u.score").Lte(3),
			And(F("u.score").Gte(3), F("u.score").Lt(5)), Or(F("u.score").Eq(0), F("u.score").Eq(5)),
			Not(F("u.score").Eq(3)), F("u.score").In(0, 4),
		} {
			indexed := all(t, tx.From("users_i").As("u").Where(predicate).OrderBy(Asc(F("u.score"))))
			scanned := all(t, tx.From("users_s").As("u").Where(predicate).OrderBy(Asc(F("u.score"))))
			if !reflect.DeepEqual(indexed, scanned) {
				t.Fatalf("indexed=%v scanned=%v", indexed, scanned)
			}
		}
		base := before.OrderBy(Desc(F("u.score")))
		page := all(t, base.Offset(1).Limit(1))
		if len(page) != 1 || page[0]["u"]["id"] != "d" {
			t.Fatal(page)
		}
		if got := all(t, base); len(got) != 4 || got[0]["u"]["id"] != "a" {
			t.Fatal(got)
		}
		queries := []struct {
			name  string
			build func(string) Query
			count int
		}{
			{"inner", func(s string) Query {
				return tx.From("users"+s).As("u").InnerJoin("orders"+s, "o", F("u.id").EqField(F("o.user_id")))
			}, 4},
			{"left-chain", func(s string) Query {
				return tx.From("users"+s).As("u").LeftJoin("orders"+s, "o", F("u.id").EqField(F("o.user_id"))).LeftJoin("payments"+s, "p", F("o.id").EqField(F("p.order_id")))
			}, 5},
			{"inner-chain", func(s string) Query {
				return tx.From("users"+s).As("u").InnerJoin("orders"+s, "o", And(F("u.id").EqField(F("o.user_id")), F("o.total").Gte(40))).InnerJoin("payments"+s, "p", F("o.id").EqField(F("p.order_id")))
			}, 2},
			{"self", func(s string) Query {
				return tx.From("users"+s).As("u").LeftJoin("users"+s, "parent", F("u.parent").EqField(F("parent.id")))
			}, 4},
		}
		for _, q := range queries {
			i, s := all(t, q.build("_i")), all(t, q.build("_s"))
			if len(i) != q.count || !reflect.DeepEqual(i, s) {
				t.Fatalf("%s indexed=%v scanned=%v", q.name, i, s)
			}
			if q.name == "left-chain" && (i[len(i)-1]["o"] != nil || i[len(i)-1]["p"] != nil) {
				t.Fatal(i)
			}
			if q.name == "self" && (i[0]["parent"]["id"] != "c" || i[1]["parent"] != nil) {
				t.Fatal(i)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
