package docid_test

import (
	"context"
	"fmt"
	"os"

	"github.com/totoer/docid"
)

func Example() {
	dir, err := os.MkdirTemp("", "docid-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	users, err := db.Collection("users")
	if err != nil {
		panic(err)
	}
	orders, err := db.Collection("orders")
	if err != nil {
		panic(err)
	}
	if err := users.Insert("u1", docid.Document{"id": "u1", "name": "Анна", "age": 28}); err != nil {
		panic(err)
	}
	if err := orders.Insert("o1", docid.Document{"user_id": "u1", "total": 1500}); err != nil {
		panic(err)
	}
	if err := orders.CreateIndex(docid.Index{Name: "by_user", Field: "user_id"}); err != nil {
		panic(err)
	}
	rows, err := db.From("users").As("u").
		InnerJoin("orders", "o", docid.F("u.id").EqField(docid.F("o.user_id"))).
		Where(docid.And(docid.F("u.age").Gte(18), docid.F("o.total").Gt(1000))).
		OrderBy(docid.Desc(docid.F("o.total"))).
		Limit(10).
		All(context.Background())
	if err != nil {
		panic(err)
	}
	for _, row := range rows {
		fmt.Println(row["u"]["name"], row["o"]["total"])
	}
	// Output: Анна 1500
}
