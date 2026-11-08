package docid

import (
	"fmt"
	"testing"
)

func benchmarkUserIndexes(b *testing.B, c *Collection) {
	b.Helper()
	for _, def := range []Index{
		{Name: "score", Field: "score"},
		{Name: "city", Field: "profile.city"},
		{Name: "email", Field: "email", Unique: true},
	} {
		benchmarkCreateIndex(b, c, def)
	}
}

func benchmarkPopulatedCollection(b *testing.B, indexed, durable bool) (*Collection, []benchmarkUser) {
	b.Helper()
	db := benchmarkDB(b, durable)
	c, users := benchmarkUsers(b, db, 128)
	if indexed {
		benchmarkUserIndexes(b, c)
	}
	return c, users
}

func BenchmarkCollection(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("Indexed=%t", indexed), func(b *testing.B) {
			c, users := benchmarkPopulatedCollection(b, indexed, false)
			b.Run("Get", func(b *testing.B) {
				b.ReportAllocs()
				var doc Document
				var err error
				i := 0
				for b.Loop() {
					doc, err = c.Get(users[i%len(users)].ID)
					if err != nil {
						b.Fatal(err)
					}
					i++
				}
				if doc["id"] != users[(i-1)%len(users)].ID {
					b.Fatal("incorrect document")
				}
			})
			b.Run("GetInto", func(b *testing.B) {
				b.ReportAllocs()
				var user benchmarkUser
				i := 0
				for b.Loop() {
					if err := c.GetInto(users[i%len(users)].ID, &user); err != nil {
						b.Fatal(err)
					}
					i++
				}
				if user.ID != users[(i-1)%len(users)].ID || len(user.Tags) != 2 {
					b.Fatal("incorrect typed document")
				}
			})
			b.Run("ParallelGet", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						id := users[i%len(users)].ID
						doc, err := c.Get(id)
						if err != nil || doc["id"] != id {
							b.Error("incorrect parallel read: ", err)
							return
						}
						i++
					}
				})
			})
			b.Run("PutReplace", func(b *testing.B) {
				// Writes need their own store on every invocation (including
				// -count repetitions), so LSM history is not shared with reads
				// or other write benchmarks.
				c, users := benchmarkPopulatedCollection(b, indexed, false)
				benchmarkPutReplace(b, c, users[0])
			})
			b.Run("InsertDelete", func(b *testing.B) {
				c, users := benchmarkPopulatedCollection(b, indexed, false)
				// One operation is a complete insert/delete lifecycle. A fixed
				// document bounds the live document and index entry counts.
				user := benchmarkUserValue(len(users))
				b.ReportAllocs()
				for b.Loop() {
					if err := c.Insert(user.ID, user); err != nil {
						b.Fatal(err)
					}
					if err := c.Delete(user.ID); err != nil {
						b.Fatal(err)
					}
				}
				if _, err := c.Get(user.ID); err != ErrNotFound {
					b.Fatalf("document remains after delete: %v", err)
				}
			})
		})
	}
}

func benchmarkPutReplace(b *testing.B, c *Collection, original benchmarkUser) {
	b.Helper()
	updated := original
	updated.Score = (updated.Score + 1) % 100
	updated.Profile.City = "Нови-Сад"
	versions := [2]benchmarkUser{updated, original}
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Put(original.ID, versions[i%2]); err != nil {
			b.Fatal(err)
		}
		i++
	}
	var got benchmarkUser
	if err := c.GetInto(original.ID, &got); err != nil {
		b.Fatal(err)
	}
	want := versions[(i-1)%2]
	if got.Score != want.Score || got.Profile != want.Profile {
		b.Fatal("replacement was not persisted")
	}
}

// This separate group includes OS filesystem latency and pebble.Sync. Compare
// it only on the same filesystem; the main module benchmarks use an in-memory FS.
func BenchmarkCollectionDurable(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("PutReplace/Indexed=%t", indexed), func(b *testing.B) {
			c, users := benchmarkPopulatedCollection(b, indexed, true)
			benchmarkPutReplace(b, c, users[0])
		})
	}
}
