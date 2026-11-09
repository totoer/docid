package docid

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/cockroachdb/pebble"
)

func stateTextIDs(t *testing.T, state *collectionState, query string) []string {
	t.Helper()
	n, err := parseTextQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := state.textIndexes["search"].search(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	return sortedTextIDs(ids)
}
func TestTextIndexDefinitions(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	for _, def := range []TextIndex{{}, {Name: "bad", Fields: nil}, {Name: "search", Fields: []string{"a", "A"}}, {Name: "search", Fields: []string{"a..b"}}, {Name: "search", Fields: []string{"body"}, Tokenizer: "porter"}} {
		if err := c.CreateTextIndex(def); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("%+v: %v", def, err)
		}
	}
	fields := []string{"body"}
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: fields}); err != nil {
		t.Fatal(err)
	}
	fields[0] = "mutated"
	if err := c.CreateIndex(Index{Name: "search", Field: "body"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatal(err)
	}
	if err := c.CreateTextIndex(TextIndex{Name: "aaa", Fields: []string{"body"}}); err != nil {
		t.Fatal(err)
	}
	defs, err := c.TextIndexes()
	if err != nil || len(defs) != 2 || defs[0].Name != "aaa" || defs[1].Fields[0] != "body" || defs[1].Tokenizer != "unicode61" {
		t.Fatalf("%+v %v", defs, err)
	}
	defs[1].Fields[0] = "mutated"
	again, _ := c.TextIndexes()
	if again[1].Fields[0] != "body" {
		t.Fatal("definition aliasing")
	}
	if err := c.DropTextIndex("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := c.DropTextIndex("search"); err != nil {
		t.Fatal(err)
	}
	scalars, _ := c.Indexes()
	if len(scalars) != 1 {
		t.Fatal("scalar index dropped")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TextIndexes(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := c.CreateTextIndex(TextIndex{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := c.DropTextIndex("aaa"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestTextIndexCRUDReopen(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	c := collection(t, db, "docs")
	insert(t, c, "a", Document{"body": "Café база данных", "unique": 1})
	insert(t, c, "b", Document{"body": "старый текст", "unique": 2})
	if err := c.CreateIndex(Index{Name: "unique", Field: "unique", Unique: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}, Tokenizer: "unicode61 remove_diacritics 0"}); err != nil {
		t.Fatal(err)
	}
	if got := stateTextIDs(t, db.collections["docs"], "cafe"); len(got) != 0 {
		t.Fatal(got)
	}
	if err := c.Put("a", Document{"body": "отклонённый", "unique": 2}); !errors.Is(err, ErrUniqueConstraint) {
		t.Fatal(err)
	}
	if got := stateTextIDs(t, db.collections["docs"], "Café"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatal(got)
	}
	if err := c.Put("a", Document{"body": "новый документ", "unique": 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("b"); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if got := stateTextIDs(t, db.collections["docs"], "новый"); !reflect.DeepEqual(got, []string{"a"}) {
			t.Fatal(got)
		}
		if got := stateTextIDs(t, db.collections["docs"], "старый OR Café"); len(got) != 0 {
			t.Fatal(got)
		}
		defs, err := collection(t, db, "docs").TextIndexes()
		if err != nil || defs[0].Tokenizer != "unicode61 remove_diacritics 0" {
			t.Fatal(defs, err)
		}
		if pass == 0 {
			db = reopenTestDB(t, db, path)
		}
	}
	if err := collection(t, db, "docs").DropTextIndex("search"); err != nil {
		t.Fatal(err)
	}
	db = reopenTestDB(t, db, path)
	defs, _ := collection(t, db, "docs").TextIndexes()
	if len(defs) != 0 {
		t.Fatal(defs)
	}
}
func TestTextIndexCorruptDefinition(t *testing.T) {
	for _, raw := range []string{`{`, `{"version":2,"profile":"fts5-unicode61-v1","definition":{"name":"search","fields":["body"]}}`, `{"version":1,"profile":"unknown","definition":{"name":"search","fields":["body"]}}`, `{"version":1,"profile":"fts5-unicode61-v1","definition":{"name":"other","fields":["body"]}}`, `{"version":1,"profile":"fts5-unicode61-v1","definition":{"name":"search","fields":[]}}`} {
		t.Run(raw, func(t *testing.T) {
			path := t.TempDir()
			db := reopenTestDB(t, nil, path)
			collection(t, db, "docs")
			if err := db.store.Set(textIndexKey("docs", "search"), []byte(raw), pebble.Sync); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path)
			if err == nil {
				opened.Close()
				t.Fatal("corruption accepted")
			}
		})
	}
}
func TestTextIndexStorageFailure(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	c := collection(t, db, "docs")
	insert(t, c, "a", Document{"body": "before"})
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ro := &DB{store: store, collections: map[string]*collectionState{}}
	t.Cleanup(func() { ro.Close() })
	if err := ro.load(); err != nil {
		t.Fatal(err)
	}
	c = &Collection{db: ro, name: "docs"}
	for _, operation := range []func() error{
		func() error { return c.CreateTextIndex(TextIndex{Name: "new", Fields: []string{"body"}}) },
		func() error { return c.DropTextIndex("search") }, func() error { return c.Put("a", Document{"body": "after"}) }, func() error { return c.Delete("a") },
	} {
		if err := operation(); !errors.Is(err, pebble.ErrReadOnly) {
			t.Fatal(err)
		}
	}
	defs, _ := c.TextIndexes()
	if len(defs) != 1 || defs[0].Name != "search" {
		t.Fatal(defs)
	}
	if got := stateTextIDs(t, ro.collections["docs"], "before"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatal(got)
	}
	doc, err := c.Get("a")
	if err != nil || doc["body"] != "before" {
		t.Fatal(doc, err)
	}
}
func TestTextTransactionPostings(t *testing.T) {
	for _, outcome := range []string{"commit", "error", "panic", "cancel", "commit-error"} {
		t.Run(outcome, func(t *testing.T) {
			path := t.TempDir()
			db := reopenTestDB(t, nil, path)
			c := collection(t, db, "docs")
			insert(t, c, "a", Document{"body": "before", "unique": 1})
			insert(t, c, "b", Document{"body": "delete", "unique": 2})
			if err := c.CreateIndex(Index{Name: "unique", Field: "unique", Unique: true}); err != nil {
				t.Fatal(err)
			}
			if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reject := errors.New("reject")
			func() {
				defer func() {
					if value := recover(); value != nil {
						if outcome != "panic" || value != reject {
							panic(value)
						}
					}
				}()
				err := db.update(ctx, func(tx *Tx) error {
					tc := txCollection(t, tx, "docs")
					if err := tc.Put("a", Document{"body": "after", "unique": 1}); err != nil {
						return err
					}
					if err := tc.Delete("b"); err != nil {
						return err
					}
					if err := tc.Insert("c", Document{"body": "created", "unique": 2}); err != nil {
						return err
					}
					for _, failure := range []error{tc.Put("a", Document{"body": "rejected", "unique": 2}), tc.Insert("a", Document{"body": "rejected"}), tc.Put("a", json.RawMessage(`null`))} {
						if failure == nil {
							t.Fatal("accepted rejected statement")
						}
					}
					if got := stateTextIDs(t, tx.state.view.collections["docs"], "after OR created"); !reflect.DeepEqual(got, []string{"a", "c"}) {
						t.Fatal(got)
					}
					if got := stateTextIDs(t, db.collections["docs"], "after OR created"); len(got) != 0 {
						t.Fatal("published before commit", got)
					}
					switch outcome {
					case "error":
						return reject
					case "panic":
						panic(reject)
					case "cancel":
						cancel()
					}
					return nil
				}, func(batch *pebble.Batch) error {
					if outcome == "commit-error" {
						return reject
					}
					return batch.Commit(pebble.Sync)
				})
				if outcome == "commit" {
					if err != nil {
						t.Fatal(err)
					}
				} else if outcome == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				} else if !errors.Is(err, reject) {
					t.Fatal(err)
				}
			}()
			for pass := 0; pass < 2; pass++ {
				query := "before"
				want := []string{"a"}
				if outcome == "commit" {
					query = "after OR created"
					want = []string{"a", "c"}
				}
				if got := stateTextIDs(t, db.collections["docs"], query); !reflect.DeepEqual(got, want) {
					t.Fatal(outcome, got, want)
				}
				if got := stateTextIDs(t, db.collections["docs"], "rejected"); len(got) != 0 {
					t.Fatal(got)
				}
				if pass == 0 {
					db = reopenTestDB(t, db, path)
				}
			}
		})
	}
}
