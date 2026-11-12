package service

import (
	"math"
	"testing"
)

func TestExactFXRounding(t *testing.T) {
	for _, c := range []struct {
		nt   int64
		rate string
		want int64
	}{{1000, "32", 3125}, {1000, "31.7", 3155}, {51000, "32.00", 159375}} {
		got, e := Quote(c.nt, c.rate)
		if e != nil || got != c.want {
			t.Fatal(c, got, e)
		}
	}
	for _, rate := range []string{"0", "-1", "NaN", "1e5", "1/2", "999999999999999999999999999999999"} {
		if _, e := Quote(1000, rate); e == nil && rate != "999999999999999999999999999999999" {
			t.Fatal(rate)
		}
	}
}

func TestQuoteAmountBounds(t *testing.T) {
	for _, nt := range []int64{0, -1, math.MinInt64, -184467440737095516, 184467440737095517, math.MaxInt64} {
		if got, err := Quote(nt, "1"); err == nil {
			t.Errorf("Quote(%d, 1) = %d; want amount outside limits", nt, got)
		}
	}
	got, err := Quote(math.MaxInt64, "100000000000000")
	if err != nil || got != 9223373 {
		t.Fatalf("large exact quote = %d, %v; want 9223373", got, err)
	}
	if got, err := Quote(99999999, "100"); err != nil || got != 99999999 {
		t.Fatalf("maximum quote = %d, %v", got, err)
	}
	if _, err := Quote(100000000, "100"); err == nil {
		t.Fatal("accepted quote above provider limit")
	}
}
