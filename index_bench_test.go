package docid

import (
	"fmt"
	"testing"
)

func benchmarkRecords(b *testing.B, n int) []record {
	b.Helper()
	records := make([]record, n)
	for i := range records {
		u := benchmarkUserValue(i)
		_, doc, err := encodeDocument(u)
		if err != nil {
			b.Fatal(err)
		}
		records[i] = record{id: u.ID, doc: doc}
	}
	return records
}

func BenchmarkIndexBuild(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			records := benchmarkRecords(b, n)
			for _, def := range []Index{
				{Name: "score", Field: "score"},
				{Name: "city", Field: "profile.city"},
				{Name: "email", Field: "email", Unique: true},
			} {
				b.Run(def.Name, func(b *testing.B) {
					b.ReportAllocs()
					var idx *index
					for b.Loop() {
						idx = newIndex(def)
						for _, item := range records {
							if err := idx.check(item.id, item.doc); err != nil {
								b.Fatal(err)
							}
							idx.add(item.id, item.doc)
						}
					}
					if idx.tree.Len() != n {
						b.Fatalf("index size=%d, want=%d", idx.tree.Len(), n)
					}
					b.ReportMetric(float64(n), "docs/op")
				})
			}
		})
	}
}

func BenchmarkIndexMatches(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("Docs=%d", n), func(b *testing.B) {
			idx := newIndex(Index{Name: "score", Field: "score"})
			for _, item := range benchmarkRecords(b, n) {
				idx.add(item.id, item.doc)
			}
			for _, tc := range []struct {
				op    string
				value int
				want  int
			}{
				{"eq", 50, n / 100}, {"eq", 101, 0},
				{"gt", 98, n / 100}, {"gte", 90, n / 10},
				{"lt", 10, n / 10}, {"lte", 0, n / 100},
			} {
				b.Run(fmt.Sprintf("%s/%d", tc.op, tc.value), func(b *testing.B) {
					value, err := literal(tc.value)
					if err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					var ids []string
					for b.Loop() {
						ids = idx.matches(tc.op, value)
					}
					if len(ids) != tc.want {
						b.Fatalf("candidates=%d, want=%d", len(ids), tc.want)
					}
					b.ReportMetric(float64(tc.want), "ids/op")
				})
			}
		})
	}
}

func BenchmarkIndexReplace(b *testing.B) {
	for _, unique := range []bool{false, true} {
		b.Run(fmt.Sprintf("Unique=%t", unique), func(b *testing.B) {
			records := benchmarkRecords(b, 1000)
			idx := newIndex(Index{Name: "email", Field: "email", Unique: unique})
			for _, item := range records {
				idx.add(item.id, item.doc)
			}
			old := records[0]
			_, updated, err := encodeDocument(map[string]any{"email": "updated@example.com"})
			if err != nil {
				b.Fatal(err)
			}
			next := updated
			b.ReportAllocs()
			for b.Loop() {
				if err := idx.check(old.id, next); err != nil {
					b.Fatal(err)
				}
				idx.remove(old.id, old.doc)
				idx.add(old.id, next)
				old.doc, next = next, old.doc
			}
			if idx.tree.Len() != len(records) {
				b.Fatal("replacement changed index size")
			}
			entry, ok := idx.entry(old.id, old.doc)
			if !ok || len(idx.matches("eq", entry.value)) != 1 {
				b.Fatal("updated index entry missing")
			}
		})
	}
}
