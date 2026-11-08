package docid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestTypedResultsMatchAll(t *testing.T) {
	db := openTest(t)
	users, orders := collection(t, db, "users"), collection(t, db, "orders")
	for i := range 12 {
		id := fmt.Sprintf("u%02d", i)
		insert(t, users, id, Document{
			"id": id, "score": i % 4, "active": i%2 == 0,
			"payload": strings.Repeat(id+"<&", i*20),
			"profile": Document{"city": "Белград", "large": json.Number("9007199254740993")},
		})
		if i%3 != 0 {
			for j := range 2 {
				insert(t, orders, fmt.Sprintf("%s-%d", id, j), Document{"user_id": id, "total": j + 1})
			}
		}
	}
	// Alias envelopes must escape arbitrary valid alias names just as JSON does.
	alias := "u\"<&\\alias"
	base := users.Find().As(alias)
	joined := base.LeftJoin("orders", "o", F(alias+".id").EqField(F("o.user_id")))
	for _, indexed := range []bool{false, true} {
		if indexed {
			if err := orders.CreateIndex(Index{Name: "user", Field: "user_id"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range []Query{
			base, base.Limit(0), base.Offset(100),
			base.Where(F(alias + ".active").Eq(true)),
			base.OrderBy(Desc(F(alias+".score")), Asc(F(alias+".id"))).Offset(2).Limit(5),
			joined,
			joined.Where(F("o.total").Gt(1)).OrderBy(Desc(F(alias + ".score"))),
		} {
			want := all(t, q)
			var rows []Row
			if err := q.AllInto(context.Background(), &rows); err != nil || !reflect.DeepEqual(rows, want) {
				t.Fatalf("AllInto indexed=%t: err=%v, got=%v, want=%v", indexed, err, rows, want)
			}
			aliases := []string{alias}
			if len(q.joins) != 0 {
				aliases = append(aliases, "o")
			}
			for _, selected := range aliases {
				wantDocs := make([]Document, len(want))
				for i := range want {
					wantDocs[i] = want[i][selected]
				}
				var docs []Document
				if err := q.SelectInto(context.Background(), selected, &docs); err != nil || !reflect.DeepEqual(docs, wantDocs) {
					t.Fatalf("SelectInto %q indexed=%t: err=%v, got=%v, want=%v", selected, indexed, err, docs, wantDocs)
				}
			}
		}
	}
}

func TestTypedQueryJSONRules(t *testing.T) {
	c := collection(t, openTest(t), "docs")
	insert(t, c, "1", json.RawMessage(`{"count":"12","created":"2026-11-08T12:00:00Z","number":9007199254740993,"raw":{"n":1}}`))
	type value struct {
		Count   int             `json:"count,string"`
		Created time.Time       `json:"created"`
		Number  any             `json:"number"`
		Raw     json.RawMessage `json:"raw"`
	}
	var want value
	if err := c.GetInto("1", &want); err != nil {
		t.Fatal(err)
	}
	for _, q := range []Query{c.Find(), c.Find().Where(F("docs.count").Exists())} {
		var got []*value
		if err := q.SelectInto(context.Background(), "docs", &got); err != nil || len(got) != 1 || !reflect.DeepEqual(*got[0], want) {
			t.Fatal(got, err)
		}
		var rows []struct {
			Doc value `json:"docs"`
		}
		if err := q.AllInto(context.Background(), &rows); err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Doc, want) {
			t.Fatal(rows, err)
		}
	}
}

// These tests are serial. A custom slice decoder must run once for the entire
// array, outside database locks, and before the destination is committed.
var queryDecodeHook func() error

type hookedDocuments []Document

func (dst *hookedDocuments) UnmarshalJSON(raw []byte) error {
	if err := queryDecodeHook(); err != nil {
		return err
	}
	type plain hookedDocuments
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode((*plain)(dst))
}

func TestTypedQueryDecodeUsesSnapshotOutsideLock(t *testing.T) {
	defer func() { queryDecodeHook = nil }()
	for _, allRows := range []bool{false, true} {
		db := openTest(t)
		c := collection(t, db, "docs")
		insert(t, c, "1", Document{"n": 1})
		insert(t, c, "2", Document{"n": 2})
		calls := 0
		queryDecodeHook = func() error {
			calls++
			if !db.mu.TryLock() {
				return errors.New("custom decoder called under database lock")
			}
			db.mu.Unlock()
			if err := c.Put("1", Document{"n": 100}); err != nil {
				return err
			}
			if err := c.Delete("2"); err != nil {
				return err
			}
			return db.Close()
		}
		var got hookedDocuments
		var err error
		want := hookedDocuments{{"n": json.Number("1")}, {"n": json.Number("2")}}
		if allRows {
			err = c.Find().AllInto(context.Background(), &got)
			want = hookedDocuments{{"docs": map[string]any(want[0])}, {"docs": map[string]any(want[1])}}
		} else {
			err = c.Find().SelectInto(context.Background(), "docs", &got)
		}
		if err != nil || calls != 1 || !reflect.DeepEqual(got, want) {
			t.Fatalf("allRows=%t: calls=%d, err=%v, got=%v", allRows, calls, err, got)
		}
	}
}

func TestTypedQueryCancellationDuringDecode(t *testing.T) {
	defer func() { queryDecodeHook = nil }()
	c := collection(t, openTest(t), "docs")
	insert(t, c, "1", Document{"n": 1})
	for _, allRows := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		queryDecodeHook = func() error { cancel(); return nil }
		got := hookedDocuments{{"old": true}}
		var err error
		if allRows {
			err = c.Find().AllInto(ctx, &got)
		} else {
			err = c.Find().SelectInto(ctx, "docs", &got)
		}
		cancel()
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, hookedDocuments{{"old": true}}) {
			t.Fatal(got, err)
		}
	}
}

func TestRecordOwnsRawBytes(t *testing.T) {
	for _, document := range []bool{false, true} {
		raw := []byte(`{"n":1}`)
		item, err := readRecord("1", raw, recordMode{raw: true, document: document})
		if err != nil {
			t.Fatal(err)
		}
		copy(raw, `{"n":2}`)
		if string(item.raw) != `{"n":1}` {
			t.Fatal("record retained borrowed storage bytes")
		}
	}
}

func TestRawPathsRejectCorruptDocuments(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "docs")
	for _, raw := range []string{`null`, `[]`, `{`, `{} {}`} {
		if err := db.store.Set(docKey("docs", "1"), []byte(raw), pebble.Sync); err != nil {
			t.Fatal(err)
		}
		var dst []Document
		for _, err := range []error{
			c.Put("1", Document{}), c.Delete("1"),
			c.Find().SelectInto(context.Background(), "docs", &dst),
			c.Find().AllInto(context.Background(), &dst),
		} {
			if !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("corrupt JSON %q: %v", raw, err)
			}
		}
	}
}

type marshaledDocument string

func (v marshaledDocument) MarshalJSON() ([]byte, error) { return []byte(v), nil }

func TestMarshalDocumentValidation(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		c := collection(t, openTest(t), "docs")
		if indexed {
			if err := c.CreateIndex(Index{Name: "n", Field: "n"}); err != nil {
				t.Fatal(err)
			}
		}
		insert(t, c, "valid", marshaledDocument(" \n {\"n\":1} \t "))
		for _, raw := range []string{`null`, `[]`, `42`, `"text"`, `{} {}`, `{`} {
			if err := c.Insert("bad", marshaledDocument(raw)); !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("indexed=%t, raw=%q: %v", indexed, raw, err)
			}
		}
		// Marshal accepts deeply nested Go containers that the JSON decoder
		// rejects. Unindexed writes must enforce that limit too.
		deep := Document{}
		for range 10001 {
			deep = Document{"nested": deep}
		}
		if err := c.Insert("deep", deep); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("indexed=%t, excessive JSON depth: %v", indexed, err)
		}
	}
}
