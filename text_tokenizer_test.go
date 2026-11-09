package docid

import (
	"encoding/hex"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestTextTokenizerFixtures(t *testing.T) {
	cases, results := loadTextFixtures(t)
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			tok, err := newUnicodeTokenizer(c.Tokenizer)
			if (err != nil) != results[i].InvalidDefinition {
				t.Fatalf("definition error %v; want invalid=%t", err, results[i].InvalidDefinition)
			}
			if err != nil {
				return
			}
			for row, d := range c.Documents {
				for col, value := range d.Values {
					var want []string
					for _, token := range results[i].Tokens {
						if token.Document == row+1 && token.Column == c.Fields[col] {
							b, _ := hex.DecodeString(token.Hex)
							if token.Offset != len(want) {
								t.Fatal("non-contiguous oracle positions")
							}
							want = append(want, string(b))
						}
					}
					var got []string
					if value != nil {
						got, err = tok.tokenize(nil, *value)
					}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("row %d column %s: tokens differ (got %d, want %d): %v", row+1, c.Fields[col], len(got), len(want), err)
					}
				}
			}
		})
	}
}

func TestTextTokenizerUnsupported(t *testing.T) {
	for _, spec := range []string{"ascii", "porter", "trigram", "", "   ", "unicode61 categories ''"} {
		_, err := newUnicodeTokenizer(spec)
		if (spec == "" || spec == "unicode61 categories ''") != (err == nil) {
			t.Fatalf("%q: %v", spec, err)
		}
	}
}

func FuzzTextTokenizer(f *testing.F) {
	for _, s := range []string{"База данных", "café e\u0301", "", "a\x00b", "𐐀 Ა"} {
		f.Add(s)
	}
	tok, _ := newUnicodeTokenizer("")
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		a, err := tok.tokenize(nil, s)
		b, err2 := tok.tokenize(nil, s)
		if err != nil || err2 != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("unstable tokenizer")
		}
		for _, term := range a {
			if len(term) > 32768 {
				t.Fatal("oversized term")
			}
		}
	})
}
