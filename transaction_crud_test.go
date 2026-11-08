package docid

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cockroachdb/pebble"
	"reflect"
	"sync"
	"testing"
)

func txCollection(t *testing.T, tx *Tx, name string) *TxCollection {
	t.Helper()
	c, err := tx.Collection(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTxAccountAndLedger(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	accounts := collection(t, db, "accounts")
	insert(t, accounts, "a", Document{"balance": 100})
	insufficient, rejected := errors.New("insufficient funds"), errors.New("reject after writes")
	for _, tc := range []struct {
		id     string
		amount int
		reject bool
		want   error
	}{
		{"success", 40, false, nil}, {"insufficient", 999, false, insufficient}, {"rollback", 10, true, rejected},
	} {
		err := db.Update(context.Background(), func(tx *Tx) error {
			account := txCollection(t, tx, "accounts")
			var value struct {
				Balance int `json:"balance"`
			}
			if err := account.GetInto("a", &value); err != nil {
				return err
			}
			if value.Balance < tc.amount {
				return insufficient
			}
			value.Balance -= tc.amount
			if err := account.Put("a", value); err != nil {
				return err
			}
			if err := txCollection(t, tx, "ledger").Insert(tc.id, Document{"account": "a", "amount": tc.amount}); err != nil {
				return err
			}
			if tc.reject {
				return rejected
			}
			return nil
		})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.id, err)
		}
		db = reopenTestDB(t, db, path)
		accounts = collection(t, db, "accounts")
		var value struct{ Balance int }
		if err := accounts.GetInto("a", &value); err != nil || value.Balance != 60 {
			t.Fatalf("%v: %v", value, err)
		}
		ledger := collection(t, db, "ledger")
		if got := all(t, ledger.Find()); len(got) != 1 || got[0]["ledger"]["amount"] != json.Number("40") {
			t.Fatal(got)
		}
	}
}

func TestTxConcurrentIncrements(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "counter")
	insert(t, c, "x", Document{"n": 0})
	const workers, increments = 8, 10
	var wg sync.WaitGroup
	done := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < increments; i++ {
				if err := db.Update(context.Background(), func(tx *Tx) error {
					tc, err := tx.Collection("counter")
					if err != nil {
						return err
					}
					var value struct {
						N int `json:"n"`
					}
					if err := tc.GetInto("x", &value); err != nil {
						return err
					}
					value.N++
					return tc.Put("x", value)
				}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	wg.Wait()
	for w := 0; w < workers; w++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var got struct{ N int }
	if err := c.GetInto("x", &got); err != nil || got.N != workers*increments {
		t.Fatalf("%v: %v", got, err)
	}
}

func TestTxUniqueIndexes(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	c := collection(t, db, "docs")
	if err := c.CreateIndex(Index{Name: "unique", Field: "v", Unique: true}); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "a", Document{"v": json.Number("9007199254740993")})
	insert(t, c, "b", Document{"v": 1})
	err := db.Update(context.Background(), func(tx *Tx) error {
		tc := txCollection(t, tx, "docs")
		for _, value := range []any{json.Number("9007199254740993"), json.Number("1e0")} {
			if err := tc.Insert("conflict", Document{"v": value}); !errors.Is(err, ErrUniqueConstraint) {
				t.Fatal(err)
			}
		}
		if err := tc.Delete("a"); err != nil {
			return err
		}
		if err := tc.Insert("a2", Document{"v": json.Number("9007199254740993")}); err != nil {
			return err
		}
		if err := tc.Insert("near", Document{"v": json.Number("9007199254740992")}); err != nil {
			return err
		}
		if err := tc.Put("b", Document{"v": 2}); err != nil {
			return err
		}
		if err := tc.Insert("c", Document{"v": json.Number("1.0")}); err != nil {
			return err
		}
		if err := tc.Put("a2", Document{"v": 1}); !errors.Is(err, ErrUniqueConstraint) {
			t.Fatal(err)
		}
		if err := tc.Insert("duplicate", Document{"v": json.Number("9007199254740993")}); !errors.Is(err, ErrUniqueConstraint) {
			t.Fatal(err)
		}
		for _, id := range []string{"missing1", "missing2"} {
			if err := tc.Insert(id, Document{}); err != nil {
				return err
			}
		}
		for _, id := range []string{"array1", "array2"} {
			if err := tc.Insert(id, Document{"v": []int{1}}); err != nil {
				return err
			}
		}
		for _, id := range []string{"object1", "object2"} {
			if err := tc.Insert(id, Document{"v": Document{"n": 1}}); err != nil {
				return err
			}
		}
		if err := tc.Insert("null1", Document{"v": nil}); err != nil {
			return err
		}
		if err := tc.Insert("null2", Document{"v": nil}); !errors.Is(err, ErrUniqueConstraint) {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		c = collection(t, db, "docs")
		for _, value := range []any{1, 2, nil, json.Number("9007199254740993"), json.Number("9007199254740992")} {
			if got := all(t, c.Find().Where(F("docs.v").Eq(value))); len(got) != 1 {
				t.Fatalf("%v: %v", value, got)
			}
		}
		if _, err := c.Get("a"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		for _, id := range []string{"conflict", "duplicate", "null2"} {
			if _, err := c.Get(id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s: %v", id, err)
			}
		}
		if pass == 0 {
			db = reopenTestDB(t, db, path)
		}
	}
}

func TestTxIndexRollback(t *testing.T) {
	for _, outcome := range []string{"error", "panic", "cancel", "commit-error"} {
		t.Run(outcome, func(t *testing.T) {
			path := t.TempDir()
			db := reopenTestDB(t, nil, path)
			c := collection(t, db, "docs")
			for _, def := range []Index{{Name: "unique", Field: "v", Unique: true}, {Name: "group", Field: "g"}} {
				if err := c.CreateIndex(def); err != nil {
					t.Fatal(err)
				}
			}
			insert(t, c, "a", Document{"v": "a", "g": 1})
			insert(t, c, "b", Document{"v": "b", "g": 1})
			before := all(t, c.Find().Where(F("docs.g").Eq(1)))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reject := errors.New("rollback documents")
			var escaped *TxCollection
			func() {
				if outcome == "panic" {
					defer func() {
						if p := recover(); p != reject {
							t.Fatalf("panic=%v", p)
						}
					}()
				}
				err := db.update(ctx, func(tx *Tx) error {
					tc := txCollection(t, tx, "docs")
					escaped = tc
					if err := tc.Put("a", Document{"v": "changed", "g": 2}); err != nil {
						return err
					}
					if err := tc.Delete("b"); err != nil {
						return err
					}
					if err := tc.Insert("c", Document{"v": "a", "g": 2}); err != nil {
						return err
					}
					if err := txCollection(t, tx, "new").Put("x", Document{}); err != nil {
						return err
					}
					switch outcome {
					case "panic":
						panic(reject)
					case "cancel":
						cancel()
						return nil
					case "commit-error":
						return nil
					}
					return reject
				}, func(*pebble.Batch) error { return reject })
				want := reject
				if outcome == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			}()
			if err := escaped.Put("late", Document{}); !errors.Is(err, ErrTxClosed) {
				t.Fatal(err)
			}
			for pass := 0; pass < 2; pass++ {
				c = collection(t, db, "docs")
				if got := all(t, c.Find().Where(F("docs.g").Eq(1))); !reflect.DeepEqual(got, before) {
					t.Fatalf("got %v want %v", got, before)
				}
				if got := all(t, c.Find().Where(F("docs.g").Eq(2))); len(got) != 0 {
					t.Fatal(got)
				}
				if err := c.Insert("conflict", Document{"v": "a"}); !errors.Is(err, ErrUniqueConstraint) {
					t.Fatal(err)
				}
				if _, err := c.Get("c"); !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				if names, err := db.Collections(); err != nil || !reflect.DeepEqual(names, []string{"docs"}) {
					t.Fatalf("%v: %v", names, err)
				}
				if pass == 0 {
					db = reopenTestDB(t, db, path)
				}
			}
		})
	}
}

func TestTxCRUD(t *testing.T) {
	db := openTest(t)
	var escaped *TxCollection
	err := db.Update(context.Background(), func(tx *Tx) error {
		c := txCollection(t, tx, "docs/ü")
		escaped = c
		if err := c.Insert("id/ü", json.RawMessage(`{"n":9007199254740993,"nested":{"x":1}}`)); err != nil {
			return err
		}
		if err := c.Insert("id/ü", Document{}); !errors.Is(err, ErrAlreadyExists) {
			t.Fatal(err)
		}
		doc, err := c.Get("id/ü")
		if err != nil || doc["n"] != json.Number("9007199254740993") {
			t.Fatalf("%v: %v", doc, err)
		}
		doc["nested"].(map[string]any)["x"] = 9
		doc, err = c.Get("id/ü")
		if err != nil || doc["nested"].(map[string]any)["x"] != json.Number("1") {
			t.Fatalf("%v: %v", doc, err)
		}
		if err := c.Put("id/ü", Document{"n": 2}); err != nil {
			return err
		}
		if doc, err := c.Get("id/ü"); err != nil || doc["n"] != json.Number("2") {
			t.Fatalf("%v: %v", doc, err)
		}
		if err := c.Delete("id/ü"); err != nil {
			return err
		}
		if _, err := c.Get("id/ü"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err := c.Delete("id/ü"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err := c.Insert("id/ü", Document{"n": 3}); err != nil {
			return err
		}
		if err := c.Put("new", Document{"n": 4}); err != nil {
			return err
		}
		if err := c.Delete("new"); err != nil {
			return err
		}
		for _, id := range []string{"", "\xff"} {
			if err := c.Put(id, Document{}); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("%q: %v", id, err)
			}
		}
		for _, bad := range []any{nil, 1, []int{1}, json.RawMessage(`null`), json.RawMessage(`{"bad":`), make(chan int)} {
			if err := c.Put("id/ü", bad); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("%T: %v", bad, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := collection(t, db, "docs/ü")
	if doc, err := c.Get("id/ü"); err != nil || doc["n"] != json.Number("3") {
		t.Fatalf("%v: %v", doc, err)
	}
	if _, err := c.Get("new"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, err := escaped.Get("id/ü"); return err },
		func() error { return escaped.Insert("x", Document{}) },
		func() error { return escaped.Put("", nil) },
		func() error { return escaped.Delete("id/ü") },
	} {
		if err := call(); !errors.Is(err, ErrTxClosed) {
			t.Fatal(err)
		}
	}
}
