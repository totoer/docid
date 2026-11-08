package docid

import (
	"encoding/json"
	"testing"
)

func BenchmarkDocument(b *testing.B) {
	user := benchmarkUserValue(42)
	raw, doc, err := encodeDocument(user)
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"Struct", user}, {"Map", doc}, {"RawMessage", json.RawMessage(raw)},
	} {
		b.Run("Encode/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			var got Document
			for b.Loop() {
				_, got, err = encodeDocument(tc.value)
				if err != nil {
					b.Fatal(err)
				}
			}
			if got["id"] != user.ID || got["score"] != json.Number("42") {
				b.Fatal("incorrect document encoding")
			}
		})
	}
	b.Run("Decode", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		var got Document
		for b.Loop() {
			got, err = decodeDocument(raw)
			if err != nil {
				b.Fatal(err)
			}
		}
		if got["score"] != json.Number("42") {
			b.Fatal("numeric precision was not preserved")
		}
	})
	for _, tc := range []struct {
		path string
		want any
	}{
		{"id", user.ID}, {"profile.city", "Белград"}, {"profile.missing", nil},
	} {
		b.Run("Lookup/"+tc.path, func(b *testing.B) {
			b.ReportAllocs()
			var value any
			var ok bool
			for b.Loop() {
				value, ok = lookup(doc, tc.path)
			}
			if value != tc.want || ok != (tc.want != nil) {
				b.Fatal("incorrect field lookup")
			}
		})
	}
}

// Normalize and compare together: this is the path used for document values in
// filters and indexes, including docid's exact decimal/exponent handling.
func BenchmarkScalarComparison(b *testing.B) {
	for _, tc := range []struct {
		name string
		a, z any
		want int
	}{
		{"String", "Белград", "Нови-Сад", -1},
		{"Integer", json.Number("42"), json.Number("43"), -1},
		{"EquivalentDecimal", json.Number("1.000"), json.Number("1e0"), 0},
		{"BeyondFloatPrecision", json.Number("9007199254740993"), json.Number("9007199254740992"), 1},
		{"HugeExponent", json.Number("1e1000000000"), json.Number("9e999999999"), 1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var got int
			for b.Loop() {
				a, aok := scalarOf(tc.a)
				z, zok := scalarOf(tc.z)
				if !aok || !zok {
					b.Fatal("invalid scalar")
				}
				got = compare(a, z)
			}
			if got != tc.want {
				b.Fatalf("comparison=%d, want=%d", got, tc.want)
			}
		})
	}
}
