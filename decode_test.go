package docid

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type typedUser struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Age     int       `json:"age"`
	Large   int64     `json:"large"`
	Created time.Time `json:"created"`
	Profile *struct {
		City string `json:"city"`
	} `json:"profile"`
	Tags  []string `json:"tags"`
	Extra any      `json:"extra"`
}

func TestGetInto(t *testing.T) {
	c := collection(t, openTest(t), "users")
	insert(t, c, "u1", json.RawMessage(`{"name":"Анна","age":28,"large":9007199254740993,"created":"2026-11-08T12:00:00Z","profile":{"city":"Белград"},"tags":["go"],"extra":9007199254740993}`))
	u := typedUser{ID: "old", Name: "old"}
	if err := c.GetInto("u1", &u); err != nil {
		t.Fatal(err)
	}
	if u.ID != "" || u.Name != "Анна" || u.Age != 28 || u.Large != 9007199254740993 ||
		!u.Created.Equal(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) ||
		u.Profile == nil || u.Profile.City != "Белград" ||
		!reflect.DeepEqual(u.Tags, []string{"go"}) || u.Extra != json.Number("9007199254740993") {
		t.Fatalf("decoded user: %+v", u)
	}
	var ptr *typedUser
	if err := c.GetInto("u1", &ptr); err != nil || ptr == nil || ptr.Name != u.Name {
		t.Fatal(ptr, err)
	}
	u.Profile.City = "changed"
	if err := c.GetInto("u1", &ptr); err != nil || ptr.Profile.City != "Белград" {
		t.Fatal(ptr, err)
	}
}

func TestGetIntoErrorsLeaveDestinationUnchanged(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "users")
	insert(t, c, "bad", Document{"name": "new", "age": "invalid", "profile": Document{"city": "new"}})
	insert(t, c, "overflow", Document{"age": json.Number("999999999999999999999999")})
	for _, id := range []string{"bad", "overflow"} {
		u := typedUser{Name: "old", Age: 42, Profile: &struct {
			City string `json:"city"`
		}{City: "old"}}
		originalProfile := u.Profile
		var typeErr *json.UnmarshalTypeError
		if err := c.GetInto(id, &u); !errors.As(err, &typeErr) {
			t.Fatalf("%s: expected JSON type error, got %v", id, err)
		}
		if u.Name != "old" || u.Age != 42 || u.Profile != originalProfile || u.Profile.City != "old" {
			t.Fatalf("destination changed: %+v", u)
		}
	}
	var nilUser *typedUser
	for _, dst := range []any{nil, nilUser, typedUser{}} {
		if err := c.GetInto("bad", dst); !errors.Is(err, ErrInvalidDestination) {
			t.Fatalf("%T: %v", dst, err)
		}
	}
	u := typedUser{Name: "old"}
	if err := c.GetInto("missing", &u); !errors.Is(err, ErrNotFound) || u.Name != "old" {
		t.Fatal(u, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.GetInto("bad", &u); !errors.Is(err, ErrClosed) || u.Name != "old" {
		t.Fatal(u, err)
	}
}

func TestQueryInto(t *testing.T) {
	db := openTest(t)
	users := collection(t, db, "users")
	orders := collection(t, db, "orders")
	insert(t, users, "u1", Document{"id": "u1", "name": "Анна", "age": 28, "large": json.Number("9007199254740993")})
	insert(t, users, "u2", Document{"id": "u2", "name": "Борис", "age": 35})
	insert(t, orders, "o1", Document{"user_id": "u1", "total": 1500})
	insert(t, orders, "o2", Document{"user_id": "u1", "total": 2000})
	ctx := context.Background()
	q := users.Find().As("u").Where(F("u.age").Gte(18)).OrderBy(Desc(F("u.age")))
	var selected []*typedUser
	if err := q.Offset(1).Limit(1).SelectInto(ctx, "u", &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Name != "Анна" || selected[0].Large != 9007199254740993 {
		t.Fatal(selected)
	}
	joined := q.LeftJoin("orders", "o", F("u.id").EqField(F("o.user_id")))
	type order struct {
		Total int `json:"total"`
	}
	var rows []struct {
		User  typedUser `json:"u"`
		Order *order    `json:"o"`
	}
	if err := joined.AllInto(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].User.Name != "Борис" || rows[0].Order != nil ||
		rows[1].Order.Total != 1500 || rows[2].Order.Total != 2000 {
		t.Fatal(rows)
	}
	var selectedOrders []*order
	if err := joined.SelectInto(ctx, "o", &selectedOrders); err != nil ||
		len(selectedOrders) != 3 || selectedOrders[0] != nil || selectedOrders[1].Total != 1500 {
		t.Fatal(selectedOrders, err)
	}
	var repeated []typedUser
	if err := joined.SelectInto(ctx, "u", &repeated); err != nil || len(repeated) != 3 {
		t.Fatal(repeated, err)
	}
	for _, empty := range []Query{q.Limit(0), q.Where(F("u.age").Gt(100))} {
		if err := empty.SelectInto(ctx, "u", &selected); err != nil || selected == nil || len(selected) != 0 {
			t.Fatal(selected, err)
		}
		if err := empty.AllInto(ctx, &rows); err != nil || rows == nil || len(rows) != 0 {
			t.Fatal(rows, err)
		}
		if err := empty.SelectInto(ctx, "missing", &selected); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
}

func TestQueryIntoErrorsLeaveDestinationUnchanged(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "users")
	insert(t, c, "1", Document{"age": 1})
	insert(t, c, "2", Document{"age": "bad"})
	ctx := context.Background()
	selected := []typedUser{{Name: "old"}}
	var typeErr *json.UnmarshalTypeError
	if err := c.Find().SelectInto(ctx, "users", &selected); !errors.As(err, &typeErr) || len(selected) != 1 || selected[0].Name != "old" {
		t.Fatal(selected, err)
	}
	rows := []struct {
		User typedUser `json:"users"`
	}{{User: typedUser{Name: "old"}}}
	if err := c.Find().AllInto(ctx, &rows); !errors.As(err, &typeErr) || len(rows) != 1 || rows[0].User.Name != "old" {
		t.Fatal(rows, err)
	}
	var nilSlice *[]typedUser
	for _, dst := range []any{nil, nilSlice, []typedUser{}, &typedUser{}} {
		if err := c.Find().AllInto(ctx, dst); !errors.Is(err, ErrInvalidDestination) {
			t.Fatalf("%T: %v", dst, err)
		}
		if err := c.Find().SelectInto(ctx, "users", dst); !errors.Is(err, ErrInvalidDestination) {
			t.Fatalf("%T: %v", dst, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	cases := []struct {
		q   Query
		ctx context.Context
		err error
	}{
		{c.Find(), nil, ErrInvalidQuery},
		{Query{}, ctx, ErrInvalidQuery},
		{c.Find().Limit(-1), ctx, ErrInvalidQuery},
		{db.From("missing"), ctx, ErrCollectionNotFound},
		{c.Find(), canceled, context.Canceled},
	}
	for _, tc := range cases {
		if err := tc.q.SelectInto(tc.ctx, "users", &selected); !errors.Is(err, tc.err) || selected[0].Name != "old" {
			t.Fatal(selected, err)
		}
		if err := tc.q.AllInto(tc.ctx, &rows); !errors.Is(err, tc.err) || rows[0].User.Name != "old" {
			t.Fatal(rows, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Find().SelectInto(ctx, "users", &selected); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Find().AllInto(ctx, &rows); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
