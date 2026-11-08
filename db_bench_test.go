package docid

import (
	"fmt"
	"testing"
)

// Measure the recovery module called by Open, excluding Pebble startup and disk
// latency. Every iteration rebuilds fresh collection metadata and index trees.
func BenchmarkDBLoad(b *testing.B) {
	for _, n := range []int{100, 1000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("Docs=%d/Indexed=%t", n, indexed), func(b *testing.B) {
				db := benchmarkDB(b, false)
				c, _ := benchmarkUsers(b, db, n)
				if indexed {
					benchmarkUserIndexes(b, c)
				}
				b.ReportAllocs()
				for b.Loop() {
					db.collections = make(map[string]map[string]*index)
					if err := db.load(); err != nil {
						b.Fatal(err)
					}
				}
				indexes, ok := db.collections["users"]
				if !ok {
					b.Fatal("collection was not restored")
				}
				if indexed {
					if len(indexes) != 3 {
						b.Fatal("index definitions were not restored")
					}
					for _, idx := range indexes {
						if idx.tree.Len() != n {
							b.Fatal("index entries were not restored")
						}
					}
				} else if len(indexes) != 0 {
					b.Fatal("unexpected indexes")
				}
			})
		}
	}
}

func BenchmarkIndexCreateDrop(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			db := benchmarkDB(b, false)
			c, _ := benchmarkUsers(b, db, n)
			def := Index{Name: "email", Field: "email", Unique: true}
			b.ReportAllocs()
			for b.Loop() {
				if err := c.CreateIndex(def); err != nil {
					b.Fatal(err)
				}
				if err := c.DropIndex(def.Name); err != nil {
					b.Fatal(err)
				}
			}
			defs, err := c.Indexes()
			if err != nil || len(defs) != 0 {
				b.Fatalf("index remains after drop: %v", err)
			}
		})
	}
}
