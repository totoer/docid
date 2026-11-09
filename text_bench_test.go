package docid

import (
	"context"
	"fmt"
	"runtime"
	"testing"
)

func benchmarkTextIndex(b *testing.B) *textIndex {
	b.Helper()
	idx, err := newTextIndex(TextIndex{Name: "search", Fields: []string{"title", "body"}})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := idx.add(fmt.Sprintf("%06d", i), Document{"title": "база данных", "body": fmt.Sprintf("документ номер %d о поиске и хранении документов", i)}); err != nil {
			b.Fatal(err)
		}
	}
	return idx
}
func BenchmarkTextPostings(b *testing.B) {
	for _, query := range []string{"база", "докум*", `"база данных"`, "NEAR(поиске хранении, 3)"} {
		b.Run(query, func(b *testing.B) {
			idx := benchmarkTextIndex(b)
			n, err := parseTextQuery(query)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := idx.search(context.Background(), n); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkTextPostingsBuild(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		benchmarkTextIndex(b)
	}
}
func BenchmarkTextPostingsReplace(b *testing.B) {
	idx := benchmarkTextIndex(b)
	a, _ := idx.entries(nil, "replace", Document{"body": "база данных"})
	z, _ := idx.entries(nil, "replace", Document{"body": "текст документа"})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		idx.apply(a, z)
		idx.apply(z, a)
	}
}

func BenchmarkTextIndexCreateDrop(b *testing.B) {
	db := benchmarkDB(b, false)
	c, err := db.Collection("docs")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := c.Put(fmt.Sprint(i), Document{"body": "база данных и поиск документов"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
			b.Fatal(err)
		}
		if err := c.DropTextIndex("search"); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkTextOpen(b *testing.B) {
	path := b.TempDir()
	db, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	err = db.Update(context.Background(), func(tx *Tx) error {
		c, err := tx.Collection("docs")
		if err != nil {
			return err
		}
		for i := 0; i < 1000; i++ {
			if err := c.Put(fmt.Sprint(i), Document{"body": "база данных и поиск документов"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	c, err := db.Collection("docs")
	if err != nil {
		b.Fatal(err)
	}
	if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
		b.Fatal(err)
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		db, err := Open(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkTextCRUD(b *testing.B) {
	for _, kind := range []string{"Put", "DeleteInsert", "Transaction"} {
		b.Run(kind, func(b *testing.B) {
			db := benchmarkDB(b, false)
			c, err := db.Collection("docs")
			if err != nil {
				b.Fatal(err)
			}
			if err := c.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
				b.Fatal(err)
			}
			if err := c.Put("a", Document{"body": "старый документ"}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				var err error
				switch kind {
				case "Put":
					err = c.Put("a", Document{"body": "новый документ"})
				case "DeleteInsert":
					err = c.Delete("a")
					if err == nil {
						err = c.Insert("a", Document{"body": "новый документ"})
					}
				case "Transaction":
					err = db.Update(context.Background(), func(tx *Tx) error {
						c, err := tx.Collection("docs")
						if err != nil {
							return err
						}
						return c.Put("a", Document{"body": "новый документ"})
					})
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTextQuery(b *testing.B) {
	for _, kind := range []string{"Filter", "Combined", "Join", "Pagination"} {
		b.Run(kind, func(b *testing.B) {
			db := benchmarkDB(b, false)
			docs, err := db.Collection("docs")
			if err != nil {
				b.Fatal(err)
			}
			orders, err := db.Collection("orders")
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 1000; i++ {
				id := fmt.Sprintf("%06d", i)
				if err := docs.Put(id, Document{"id": id, "body": "база данных и поиск документов", "age": i % 10}); err != nil {
					b.Fatal(err)
				}
				if err := orders.Put(id, Document{"doc_id": id}); err != nil {
					b.Fatal(err)
				}
			}
			if err := docs.CreateTextIndex(TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
				b.Fatal(err)
			}
			if err := orders.CreateIndex(Index{Name: "doc", Field: "doc_id"}); err != nil {
				b.Fatal(err)
			}
			q := docs.Find().As("d").Where(Match("d", "search", `"база данных"`))
			switch kind {
			case "Combined":
				q = q.Where(F("d.age").Gte(5))
			case "Join":
				q = q.InnerJoin("orders", "o", F("d.id").EqField(F("o.doc_id")))
			case "Pagination":
				q = q.OrderBy(Desc(F("d.age"))).Offset(20).Limit(10)
			}
			b.ReportAllocs()
			b.ResetTimer()
			want := 1000
			if kind == "Combined" {
				want = 500
			}
			if kind == "Pagination" {
				want = 10
			}
			for b.Loop() {
				if rows, err := q.All(context.Background()); err != nil || len(rows) != want {
					b.Fatal(err)
				}
			}
		})
	}
}

// This benchmark measures retained heap, not latency. GC and MemStats are
// intentionally inside each iteration; compare retained-B/index across runs.
func BenchmarkTextRetainedMemory(b *testing.B) {
	var retained int64
	for b.Loop() {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		idx := benchmarkTextIndex(b)
		runtime.GC()
		runtime.ReadMemStats(&after)
		retained += int64(after.HeapAlloc) - int64(before.HeapAlloc)
		runtime.KeepAlive(idx)
	}
	b.ReportMetric(float64(retained)/float64(b.N), "retained-B/index")
}
