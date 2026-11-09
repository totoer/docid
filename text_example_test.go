package docid_test

import (
	"context"
	"fmt"
	"os"

	"github.com/totoer/docid"
)

func ExampleCollection_CreateTextIndex() {
	dir, err := os.MkdirTemp("", "docid-text-index-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	docs, err := db.Collection("docs")
	if err != nil {
		panic(err)
	}
	if err := docs.Insert("a", docid.Document{"body": "База данных"}); err != nil {
		panic(err)
	}
	if err := docs.CreateTextIndex(docid.TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
		panic(err)
	}
	defs, err := docs.TextIndexes()
	if err != nil {
		panic(err)
	}
	fmt.Println(defs[0].Name, defs[0].Fields, defs[0].Tokenizer)
	// Output: search [body] unicode61
}

func ExampleMatch() {
	dir, err := os.MkdirTemp("", "docid-match-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	docs, err := db.Collection("docs")
	if err != nil {
		panic(err)
	}
	if err := docs.CreateTextIndex(docid.TextIndex{Name: "search", Fields: []string{"title", "body"}}); err != nil {
		panic(err)
	}
	if err := docs.Insert("a", docid.Document{"title": "Документы", "body": "Встроенная база данных"}); err != nil {
		panic(err)
	}
	var found []struct {
		Title string `json:"title"`
	}
	err = docs.Find().As("d").Where(docid.Match("d", "search", `title:докум* AND body:"база данных"`)).SelectInto(context.Background(), "d", &found)
	if err != nil {
		panic(err)
	}
	for _, doc := range found {
		fmt.Println(doc.Title)
	}
	// Output: Документы
}

func ExampleTx_From_textSearch() {
	dir, err := os.MkdirTemp("", "docid-text-tx-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	db, err := docid.Open(dir)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	docs, err := db.Collection("docs")
	if err != nil {
		panic(err)
	}
	if err := docs.CreateTextIndex(docid.TextIndex{Name: "search", Fields: []string{"body"}}); err != nil {
		panic(err)
	}
	err = db.Update(context.Background(), func(tx *docid.Tx) error {
		docs, err := tx.Collection("docs")
		if err != nil {
			return err
		}
		if err := docs.Put("a", docid.Document{"body": "база данных"}); err != nil {
			return err
		}
		rows, err := tx.From("docs").As("d").Where(docid.Match("d", "search", `"база данных"`)).All(context.Background())
		if err != nil {
			return err
		}
		fmt.Println(len(rows))
		return nil
	})
	if err != nil {
		panic(err)
	}
	// Output: 1
}
