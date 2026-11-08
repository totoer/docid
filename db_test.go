package docid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func collection(t *testing.T, db *DB, name string) *Collection {
	t.Helper()
	c, err := db.Collection(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func insert(t *testing.T, c *Collection, id string, value any) {
	t.Helper()
	if err := c.Insert(id, value); err != nil {
		t.Fatal(err)
	}
}

func all(t *testing.T, q Query) []Row {
	t.Helper()
	rows, err := q.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCRUDAndJSONStorage(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "users/ü")
	input := json.RawMessage(`{"name":"Анна","age":9007199254740993,"nested":{"ok":true},"null":null}`)
	insert(t, c, "id/1", input)
	if err := c.Insert("id/1", Document{}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatal(err)
	}
	doc, err := c.Get("id/1")
	if err != nil {
		t.Fatal(err)
	}
	if doc["age"] != json.Number("9007199254740993") {
		t.Fatal(doc)
	}
	raw, closer, err := db.store.Get(docKey(c.name, "id/1"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 || stored["name"] != "Анна" {
		t.Fatal(stored)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	doc["name"] = "changed"
	doc["nested"].(map[string]any)["ok"] = false
	fresh, err := c.Get("id/1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh["name"] != "Анна" || fresh["nested"].(map[string]any)["ok"] != true {
		t.Fatal(fresh)
	}
	if err := c.Put("id/1", struct {
		Name string `json:"name"`
	}{"Борис"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ = c.Get("id/1")
	if len(fresh) != 1 {
		t.Fatal(fresh)
	}
	if err := c.Put("new", Document{"x": true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("id/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("id/1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := c.Delete("id/1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	other := collection(t, db, "other")
	insert(t, other, "new", Document{"x": false})
	if len(all(t, c.Find())) != 1 {
		t.Fatal("collections overlap")
	}
	names, err := db.Collections()
	if err != nil || !reflect.DeepEqual(names, []string{"other", "users/ü"}) {
		t.Fatal(names, err)
	}
}

func TestInvalidDocumentsAndNames(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "c")
	for _, value := range []any{nil, 12, "text", []any{1}, json.RawMessage(`null`), json.RawMessage(`{} {}`), json.RawMessage(`{"a":`), map[string]any{"n": make(chan int)}} {
		if err := c.Insert("bad", value); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("%T: %v", value, err)
		}
	}
	for _, name := range []string{"", " spaced ", string([]byte{0xff})} {
		if _, err := db.Collection(name); !errors.Is(err, ErrInvalidName) {
			t.Fatal(name, err)
		}
	}
	if err := c.Insert("", Document{}); !errors.Is(err, ErrInvalidName) {
		t.Fatal(err)
	}
	for _, path := range []string{"", ".field", "field.", "a..b", string([]byte{0xff})} {
		if err := c.CreateIndex(Index{Name: "bad", Field: path}); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("path %q: %v", path, err)
		}
		if _, err := c.Find().Where(F("c." + path).Eq(1)).All(context.Background()); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	if len(all(t, c.Find())) != 0 {
		t.Fatal("invalid writes persisted")
	}
}

func TestIndexMaintenanceAndUniqueness(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "users")
	insert(t, c, "a", Document{"email": "a@x", "age": 10})
	insert(t, c, "b", Document{"email": "b@x", "age": 20})
	if err := c.CreateIndex(Index{Name: "email", Field: "email", Unique: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateIndex(Index{Name: "age", Field: "age"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateIndex(Index{Name: "age", Field: "age"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatal(err)
	}
	if err := c.Insert("c", Document{"email": "a@x"}); !errors.Is(err, ErrUniqueConstraint) {
		t.Fatal(err)
	}
	if _, err := c.Get("c"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := c.Put("b", Document{"email": "a@x", "age": 30}); !errors.Is(err, ErrUniqueConstraint) {
		t.Fatal(err)
	}
	if got := all(t, c.Find().Where(F("users.age").Eq(20))); len(got) != 1 {
		t.Fatal(got)
	}
	if err := c.Put("a", Document{"email": "a@x", "age": 30}); err != nil {
		t.Fatal(err)
	}
	if got := all(t, c.Find().Where(F("users.age").Eq(10))); len(got) != 0 {
		t.Fatal(got)
	}
	if got := all(t, c.Find().Where(F("users.age").Eq(30))); len(got) != 1 {
		t.Fatal(got)
	}
	if err := c.Put("a", Document{"age": 40}); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "c", Document{"email": "a@x"})
	if err := c.Delete("c"); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "d", Document{"email": "a@x"})
	if err := c.DropIndex("email"); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "e", Document{"email": "a@x"})
	if err := c.CreateIndex(Index{Name: "email", Field: "email", Unique: true}); !errors.Is(err, ErrUniqueConstraint) {
		t.Fatal(err)
	}
	defs, err := c.Indexes()
	if err != nil || len(defs) != 1 || defs[0].Name != "age" {
		t.Fatal(defs, err)
	}
}

func TestReopen(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := collection(t, db, "users")
	insert(t, c, "1", Document{"email": "a@x", "profile": Document{"age": 21}})
	if err := c.CreateIndex(Index{Name: "email", Field: "email", Unique: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateIndex(Index{Name: "age", Field: "profile.age"}); err != nil {
		t.Fatal(err)
	}
	collection(t, db, "empty")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c = collection(t, db, "users")
	defs, err := c.Indexes()
	if err != nil || len(defs) != 2 {
		t.Fatal(defs, err)
	}
	if got := all(t, c.Find().Where(F("users.profile.age").Gte(18))); len(got) != 1 {
		t.Fatal(got)
	}
	if err := c.Insert("2", Document{"email": "a@x"}); !errors.Is(err, ErrUniqueConstraint) {
		t.Fatal(err)
	}
	if err := c.Put("1", Document{"email": "b@x"}); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "2", Document{"email": "a@x"})
	if err := c.DropIndex("age"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c = collection(t, db, "users")
	defs, err = c.Indexes()
	if err != nil || len(defs) != 1 {
		t.Fatal(defs, err)
	}
	names, _ := db.Collections()
	if !reflect.DeepEqual(names, []string{"empty", "users"}) {
		t.Fatal(names)
	}
}

func TestClosedAndConcurrentUse(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "c")
	if err := c.CreateIndex(Index{Name: "n", Field: "n", Unique: true}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("%d/%d", n, i)
				if err := c.Put(id, Document{"n": n*100 + i}); err != nil {
					t.Error(err)
					return
				}
				if _, err := c.Get(id); err != nil {
					t.Error(err)
					return
				}
				if _, err := c.Find().Where(F("c.n").Gte(n * 100)).All(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}(n)
	}
	wg.Wait()
	if len(all(t, c.Find())) != 96 {
		t.Fatal("lost writes")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	checks := []func() error{
		func() error { _, err := c.Get("x"); return err },
		func() error { return c.Insert("x", Document{}) },
		func() error { return c.Put("x", Document{}) },
		func() error { return c.Delete("x") },
		func() error { return c.CreateIndex(Index{Name: "x", Field: "x"}) },
		func() error { return c.DropIndex("n") },
		func() error { _, err := c.Indexes(); return err },
		func() error { _, err := c.Find().All(context.Background()); return err },
		func() error { _, err := db.Collection("x"); return err },
		func() error { _, err := db.Collections(); return err },
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	}
}
