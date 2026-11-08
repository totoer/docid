package docid

import (
	"encoding/json"
	"math/big"
	"math/rand"
	"testing"
)

func TestExactDecimalComparison(t *testing.T) {
	pairs := []struct {
		a, b string
		cmp  int
	}{
		{"0", "-0.0", 0}, {"1", "1.000e0", 0}, {"10", "1e1", 0},
		{"-0.001", "-0.0001", -1}, {"9007199254740993", "9007199254740992", 1},
		{"1e1000000000", "9e999999999", 1}, {"-1e-1000000000", "0", -1},
		{"1e9999999999999999999999999", "1e9999999999999999999999998", 1},
		{"1e-9999999999999999999999999", "1e-9999999999999999999999998", -1},
		{"0.01000", "1e-2", 0}, {"1.2", "1.21", -1}, {"-1.2", "-1.21", 1},
	}
	for _, pair := range pairs {
		a, err := literal(json.Number(pair.a))
		if err != nil {
			t.Fatal(err)
		}
		b, err := literal(json.Number(pair.b))
		if err != nil {
			t.Fatal(err)
		}
		if got := compare(a, b); got != pair.cmp {
			t.Fatalf("%s vs %s: %d", pair.a, pair.b, got)
		}
	}
	// Independent rational oracle for bounded random JSON decimal values.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		a := json.Number(big.NewInt(rng.Int63n(2000000)-1000000).String() + "e" + big.NewInt(rng.Int63n(40)-20).String())
		b := json.Number(big.NewInt(rng.Int63n(2000000)-1000000).String() + "e" + big.NewInt(rng.Int63n(40)-20).String())
		sa, err := literal(a)
		if err != nil {
			t.Fatal(err)
		}
		sb, err := literal(b)
		if err != nil {
			t.Fatal(err)
		}
		ra, ok := new(big.Rat).SetString(string(a))
		if !ok {
			t.Fatal(a)
		}
		rb, ok := new(big.Rat).SetString(string(b))
		if !ok {
			t.Fatal(b)
		}
		if compare(sa, sb) != ra.Cmp(rb) {
			t.Fatalf("decimal mismatch: %s %s", a, b)
		}
	}
}

func TestUniqueNumericAndSparseIndex(t *testing.T) {
	db := openTest(t)
	c := collection(t, db, "c")
	if err := c.CreateIndex(Index{Name: "value", Field: "v", Unique: true}); err != nil {
		t.Fatal(err)
	}
	insert(t, c, "1", Document{"v": json.Number("1e1")})
	if err := c.Insert("2", Document{"v": 10}); err == nil {
		t.Fatal("numeric duplicate accepted")
	}
	insert(t, c, "3", Document{})
	insert(t, c, "4", Document{})
	insert(t, c, "5", Document{"v": []int{1}})
	insert(t, c, "6", Document{"v": nil})
	if err := c.Insert("7", Document{"v": nil}); err == nil {
		t.Fatal("duplicate null accepted")
	}
}
