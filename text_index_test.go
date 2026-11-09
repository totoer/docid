package docid

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func textFixtureDoc(c textFixtureCase, d textFixtureDocument) Document {
	doc := Document{}
	for i, path := range c.Fields {
		if d.Values[i] == nil {
			continue
		}
		parts := strings.Split(path, ".")
		obj := map[string]any(doc)
		for _, p := range parts[:len(parts)-1] {
			v, ok := obj[p].(map[string]any)
			if !ok {
				v = map[string]any{}
				obj[p] = v
			}
			obj = v
		}
		obj[parts[len(parts)-1]] = *d.Values[i]
	}
	return doc
}
func sortedTextIDs(ids textIDs) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func TestTextIndexFixtures(t *testing.T) {
	cases, results := loadTextFixtures(t)
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			idx, err := newTextIndex(TextIndex{Name: "search", Fields: c.Fields, Tokenizer: c.Tokenizer})
			if (err != nil) != results[i].InvalidDefinition {
				t.Fatalf("definition: %v", err)
			}
			if err != nil {
				return
			}
			for _, d := range c.Documents {
				if err := idx.add(d.ID, textFixtureDoc(c, d)); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range results[i].Queries {
				n, err := parseTextQuery(q.Query)
				var ids textIDs
				if err == nil {
					ids, err = idx.search(context.Background(), n)
				}
				if (err != nil) != q.Error {
					t.Errorf("%q error=%v want=%t", shortText(q.Query), err, q.Error)
					continue
				}
				if err == nil && !reflect.DeepEqual(sortedTextIDs(ids), q.IDs) {
					t.Errorf("%q got %v want %v", shortText(q.Query), sortedTextIDs(ids), q.IDs)
				}
			}
		})
	}
}
func TestTextIndexCloneAndProjection(t *testing.T) {
	fields := []string{"body"}
	idx, _ := newTextIndex(TextIndex{Name: "search", Fields: fields})
	fields[0] = "changed"
	if err := idx.add("a", Document{"body": "a a b"}); err != nil {
		t.Fatal(err)
	}
	for i, value := range []any{nil, false, 123, []string{"a"}, map[string]any{"body": "a"}} {
		if err := idx.add(string(rune('b'+i)), Document{"body": value}); err != nil {
			t.Fatal(err)
		}
	}
	clone := idx.clone()
	old, _ := clone.entries(nil, "a", Document{"body": "a a b"})
	next, _ := clone.entries(nil, "a", Document{"body": "c"})
	clone.apply(old, next)
	n, _ := parseTextQuery("a")
	ids, _ := idx.search(context.Background(), n)
	other, _ := clone.search(context.Background(), n)
	if !reflect.DeepEqual(sortedTextIDs(ids), []string{"a"}) || len(other) != 0 {
		t.Fatal("clone or JSON projection leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := idx.search(ctx, n); err != context.Canceled {
		t.Fatal(err)
	}
}
