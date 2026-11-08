package docid_test

import (
	"context"
	"errors"
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

func ExampleDB_Update() {
	dir, err := os.MkdirTemp("", "docid-transaction-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	accounts, err := db.Collection("accounts")
	if err != nil {
		panic(err)
	}
	type Account struct {
		Balance int `json:"balance"`
	}
	if err := accounts.Put("a1", Account{Balance: 100}); err != nil {
		panic(err)
	}
	err = db.Update(context.Background(), func(tx *docid.Tx) error {
		accounts, err := tx.Collection("accounts")
		if err != nil {
			return err
		}
		var account Account
		if err := accounts.GetInto("a1", &account); err != nil {
			return err
		}
		if account.Balance < 40 {
			return errors.New("insufficient balance")
		}
		account.Balance -= 40
		if err := accounts.Put("a1", account); err != nil {
			return err
		}
		ledger, err := tx.Collection("ledger")
		if err != nil {
			return err
		}
		return ledger.Insert("op1", docid.Document{"account_id": "a1", "amount": 40})
	})
	if err != nil {
		panic(err)
	}
	var account Account
	if err := accounts.GetInto("a1", &account); err != nil {
		panic(err)
	}
	rows, err := db.From("ledger").All(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(account.Balance, len(rows))
	// Output: 60 1
}

func ExampleTx_From() {
	dir, err := os.MkdirTemp("", "docid-transaction-query-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	orders, err := db.Collection("orders")
	if err != nil {
		panic(err)
	}
	if err := orders.CreateIndex(docid.Index{Name: "by_user", Field: "user_id"}); err != nil {
		panic(err)
	}
	var rows []docid.Row
	err = db.Update(context.Background(), func(tx *docid.Tx) error {
		users, err := tx.Collection("users")
		if err != nil {
			return err
		}
		orders, err := tx.Collection("orders")
		if err != nil {
			return err
		}
		if err := users.Put("a1", docid.Document{"id": "a1", "name": "Анна"}); err != nil {
			return err
		}
		if err := orders.Put("o1", docid.Document{"user_id": "a1", "total": 40}); err != nil {
			return err
		}
		rows, err = tx.From("users").As("u").
			InnerJoin("orders", "o", docid.F("u.id").EqField(docid.F("o.user_id"))).
			Where(docid.F("o.total").Gt(0)).All(context.Background())
		return err
	})
	if err != nil {
		panic(err)
	}
	for _, row := range rows {
		fmt.Println(row["u"]["name"], row["o"]["total"])
	}
	// Output: Анна 40
}
