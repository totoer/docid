package docid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

var txDecodeHook func() error

type txRawJSON struct{ Raw string }

func (v *txRawJSON) UnmarshalJSON(raw []byte) error {
	if txDecodeHook != nil {
		if err := txDecodeHook(); err != nil {
			return err
		}
	}
	v.Raw = string(raw)
	return nil
}

func TestTxTypedQueryCancellation(t *testing.T) {
	for _, source := range []string{"tx", "query"} {
		for _, allRows := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/all=%t", source, allRows), func(t *testing.T) {
				db := openTest(t)
				defer func() { txDecodeHook = nil }()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var txCtx, queryCtx context.Context = context.Background(), context.Background()
				if source == "tx" {
					txCtx = ctx
				} else {
					queryCtx = ctx
				}
				err := db.Update(txCtx, func(tx *Tx) error {
					c := txCollection(t, tx, "docs")
					if err := c.Put("a", Document{"n": 1}); err != nil {
						return err
					}
					q := c.Find().As("d")
					txDecodeHook = func() error { cancel(); return nil }
					if allRows {
						type row struct{ D txRawJSON }
						dst := []row{{D: txRawJSON{Raw: "unchanged"}}}
						if err := q.AllInto(queryCtx, &dst); !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(dst, []row{{D: txRawJSON{Raw: "unchanged"}}}) {
							t.Fatal(dst)
						}
					} else {
						dst := []txRawJSON{{Raw: "unchanged"}}
						if err := q.SelectInto(queryCtx, "d", &dst); !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(dst, []txRawJSON{{Raw: "unchanged"}}) {
							t.Fatal(dst)
						}
					}
					txDecodeHook = nil
					if source == "query" {
						return c.Put("handled", Document{})
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
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTxGetInto(t *testing.T) {
	db := openTest(t)
	defer func() { txDecodeHook = nil }()
	var escaped *TxCollection
	err := db.Update(context.Background(), func(tx *Tx) error {
		c := txCollection(t, tx, "docs")
		escaped = c
		raw := json.RawMessage(`{"n":9007199254740993,"extra":1.0,"nested":{"x":true}}`)
		if err := c.Put("x", raw); err != nil {
			return err
		}
		var typed struct {
			N       int64
			Extra   any
			Nested  struct{ X bool }
			Missing int
		}
		typed.Missing = 99
		if err := c.GetInto("x", &typed); err != nil {
			return err
		}
		if typed.N != 9007199254740993 || typed.Extra != json.Number("1.0") || !typed.Nested.X || typed.Missing != 0 {
			t.Fatal(typed)
		}
		var got txRawJSON
		txDecodeHook = func() error {
			// A decoder may change the transaction, but it must still decode
			// the JSON copied before the hook, rather than the replacement.
			return c.Put("x", Document{"n": 2})
		}
		if err := c.GetInto("x", &got); err != nil {
			return err
		}
		txDecodeHook = nil
		if got.Raw != string(raw) {
			t.Fatal(got.Raw)
		}
		bad := struct{ N bool }{N: true}
		before := bad
		err := c.GetInto("x", &bad)
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) || bad != before {
			t.Fatalf("%v: %v", bad, err)
		}
		if err := c.GetInto("x", nil); !errors.Is(err, ErrInvalidDestination) {
			t.Fatal(err)
		}
		if err := c.GetInto("missing", &got); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := escaped.GetInto("x", nil); !errors.Is(err, ErrTxClosed) {
		t.Fatal(err)
	}
}

func TestTxGetIntoCancellation(t *testing.T) {
	db := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { txDecodeHook = nil }()
	dst := txRawJSON{Raw: "unchanged"}
	before := dst
	err := db.Update(ctx, func(tx *Tx) error {
		c := txCollection(t, tx, "docs")
		if err := c.Put("x", Document{"n": 1}); err != nil {
			return err
		}
		txDecodeHook = func() error { cancel(); return nil }
		if err := c.GetInto("x", &dst); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dst, before) {
			t.Fatal(dst)
		}
		return nil // Cancellation of Update still prohibits commit.
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if names, err := db.Collections(); err != nil || len(names) != 0 {
		t.Fatalf("%v: %v", names, err)
	}
}

func TestTxTypedQuerySnapshot(t *testing.T) {
	for _, mode := range []string{"plain-select", "filtered-select", "all"} {
		t.Run(mode, func(t *testing.T) {
			db := openTest(t)
			c := collection(t, db, "docs")
			if err := c.CreateIndex(Index{Name: "n", Field: "n"}); err != nil {
				t.Fatal(err)
			}
			defer func() { txDecodeHook = nil }()
			err := db.Update(context.Background(), func(tx *Tx) error {
				c := txCollection(t, tx, "docs")
				raw := []string{`{"id":"a","n":1.0}`, `{"id":"b","n":2e0}`}
				for i, id := range []string{"a", "b"} {
					if err := c.Put(id, json.RawMessage(raw[i])); err != nil {
						return err
					}
				}
				q := c.Find().As("d")
				if mode == "filtered-select" {
					q = q.Where(F("d.n").Gte(1)).OrderBy(Asc(F("d.n")))
				}
				calls := 0
				txDecodeHook = func() error {
					calls++
					if calls == 1 {
						if err := c.Put("a", Document{"id": "a", "n": 100}); err != nil {
							return err
						}
						return c.Delete("b")
					}
					return nil
				}
				var got []txRawJSON
				if mode == "all" {
					var rows []struct {
						D txRawJSON `json:"d"`
					}
					if err := q.AllInto(context.Background(), &rows); err != nil {
						return err
					}
					for _, row := range rows {
						got = append(got, row.D)
					}
				} else if err := q.SelectInto(context.Background(), "d", &got); err != nil {
					return err
				}
				txDecodeHook = nil
				if calls != 2 || !reflect.DeepEqual(got, []txRawJSON{{Raw: raw[0]}, {Raw: raw[1]}}) {
					t.Fatalf("calls=%d got=%v", calls, got)
				}
				if _, err := c.Get("b"); !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				dst := []struct{ N bool }{{N: true}}
				before := append([]struct{ N bool }(nil), dst...)
				err := q.SelectInto(context.Background(), "d", &dst)
				var typeErr *json.UnmarshalTypeError
				if !errors.As(err, &typeErr) || !reflect.DeepEqual(dst, before) {
					t.Fatalf("%v: %v", dst, err)
				}
				var empty []Document
				if err := q.Where(F("d.n").Lt(0)).SelectInto(context.Background(), "d", &empty); err != nil || empty == nil || len(empty) != 0 {
					t.Fatalf("%v: %v", empty, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTxTypedJoin(t *testing.T) {
	db := openTest(t)
	links := collection(t, db, "links")
	if err := links.CreateIndex(Index{Name: "user", Field: "user_id"}); err != nil {
		t.Fatal(err)
	}
	err := db.Update(context.Background(), func(tx *Tx) error {
		users := txCollection(t, tx, "users")
		for _, id := range []string{"a", "b"} {
			if err := users.Put(id, Document{"id": id}); err != nil {
				return err
			}
		}
		if err := txCollection(t, tx, "links").Put("l1", Document{"user_id": "a", "n": int64(9007199254740993)}); err != nil {
			return err
		}
		q := users.Find().As("u").LeftJoin("links", "l", F("u.id").EqField(F("l.user_id")))
		type link struct {
			UserID string `json:"user_id"`
			N      int64
		}
		var rows []struct {
			U struct{ ID string }
			L *link
		}
		if err := q.AllInto(context.Background(), &rows); err != nil {
			return err
		}
		if len(rows) != 2 || rows[0].U.ID != "a" || rows[0].L == nil || rows[0].L.N != 9007199254740993 || rows[1].L != nil {
			t.Fatal(rows)
		}
		var selected []*link
		if err := q.SelectInto(context.Background(), "l", &selected); err != nil {
			return err
		}
		if len(selected) != 2 || selected[0] == nil || selected[0].UserID != "a" || selected[1] != nil {
			t.Fatal(selected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
