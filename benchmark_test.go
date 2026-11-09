package docid

import (
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// The main benchmarks keep the real storage path, but replace the filesystem so
// fsync latency does not hide the cost of docid's modules. Durable benchmarks
// use Open and the OS filesystem instead. Neither changes production options.
func benchmarkDB(b *testing.B, durable bool) *DB {
	b.Helper()
	var db *DB
	var err error
	if durable {
		db, err = Open(b.TempDir())
	} else {
		var store *pebble.DB
		store, err = pebble.Open("bench", &pebble.Options{FS: vfs.NewMem()})
		if err == nil {
			db = &DB{store: store, collections: make(map[string]*collectionState)}
		}
	}
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := db.Close(); err != nil {
			b.Error(err)
		}
	})
	return db
}

type benchmarkProfile struct {
	City string `json:"city"`
}

type benchmarkUser struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Email   string           `json:"email"`
	Score   int              `json:"score"`
	Active  bool             `json:"active"`
	Profile benchmarkProfile `json:"profile"`
	Tags    []string         `json:"tags"`
}

func benchmarkUserValue(i int) benchmarkUser {
	return benchmarkUser{
		ID: fmt.Sprintf("u%06d", i), Name: fmt.Sprintf("User %06d", i),
		Email: fmt.Sprintf("user%06d@example.com", i), Score: i % 100,
		Active: i%2 == 0, Profile: benchmarkProfile{City: []string{"Белград", "Нови-Сад"}[i%2]},
		Tags: []string{"go", "database"},
	}
}

func benchmarkCollection(b *testing.B, db *DB, name string) *Collection {
	b.Helper()
	c, err := db.Collection(name)
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func benchmarkUsers(b *testing.B, db *DB, n int) (*Collection, []benchmarkUser) {
	b.Helper()
	c := benchmarkCollection(b, db, "users")
	users := make([]benchmarkUser, n)
	for i := range users {
		users[i] = benchmarkUserValue(i)
		if err := c.Insert(users[i].ID, users[i]); err != nil {
			b.Fatal(err)
		}
	}
	return c, users
}

func benchmarkCreateIndex(b *testing.B, c *Collection, def Index) {
	b.Helper()
	if err := c.CreateIndex(def); err != nil {
		b.Fatal(err)
	}
}

func benchmarkQuery(b *testing.B, q Query, want int) {
	b.Helper()
	ctx := context.Background()
	rows, err := q.All(ctx)
	if err != nil || len(rows) != want {
		b.Fatalf("query: rows=%d, want=%d, err=%v", len(rows), want, err)
	}
	b.ReportAllocs()
	for b.Loop() {
		rows, err = q.All(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
	if len(rows) != want {
		b.Fatalf("rows=%d, want=%d", len(rows), want)
	}
	b.ReportMetric(float64(want), "rows/op")
}
