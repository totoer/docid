package docid

import "testing"

func TestUnicode61Tables(t *testing.T) {
	for _, tc := range []struct {
		r        rune
		category int
	}{{'A', 9}, {'a', 5}, {'0', 13}, {0xe000, 31}, {0x1c90, 0}, {0x10400, 9}, {0x10ffff, 0}} {
		if got := unicode61Category(tc.r); got != tc.category {
			t.Fatalf("%U category %d, want %d", tc.r, got, tc.category)
		}
	}
	if unicode61Fold('À', 1) != 'a' || unicode61Fold('ộ', 1) != 'ộ' || unicode61Fold('ộ', 2) != 'o' || unicode61Fold(0x10400, 0) != 0x10428 {
		t.Fatal("Unicode 6.1 folding differs")
	}
}
