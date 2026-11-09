package service

import "testing"

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
