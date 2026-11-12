package pll_test

import (
	"errors"
	"math"
	"testing"

	"github.com/version2beta/pll"
)

func TestUnitsPerSecond(t *testing.T) {
	tests := []struct {
		num, den         uint64
		wantNum, wantDen uint64
		wantErr          error
	}{
		{48000, 1, 48000, 1, nil},
		{30000, 1001, 30000, 1001, nil},
		{60000, 2002, 30000, 1001, nil},
		{1000, 10, 100, 1, nil},
		{3, 8, 3, 8, nil},
		{math.MaxUint64, math.MaxUint64, 1, 1, nil},
		{math.MaxUint64, 1, math.MaxUint64, 1, nil},
		{1, math.MaxUint64, 1, math.MaxUint64, nil},
		{0, 1, 0, 0, pll.ErrInvalidRate},
		{1, 0, 0, 0, pll.ErrInvalidRate},
		{0, 0, 0, 0, pll.ErrInvalidRate},
	}
	for _, tt := range tests {
		r, err := pll.UnitsPerSecond(tt.num, tt.den)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("UnitsPerSecond(%d, %d): error %v, want %v", tt.num, tt.den, err, tt.wantErr)
			}
			if r != (pll.Rate{}) {
				t.Errorf("UnitsPerSecond(%d, %d): returned %d/%d with an error, want the zero Rate", tt.num, tt.den, r.Num(), r.Den())
			}
			continue
		}
		if err != nil {
			t.Errorf("UnitsPerSecond(%d, %d): unexpected error %v", tt.num, tt.den, err)
			continue
		}
		if r.Num() != tt.wantNum || r.Den() != tt.wantDen {
			t.Errorf("UnitsPerSecond(%d, %d) = %d/%d, want %d/%d (reduced to lowest terms)",
				tt.num, tt.den, r.Num(), r.Den(), tt.wantNum, tt.wantDen)
		}
	}
}

func TestBitsPerSecond(t *testing.T) {
	tests := []struct {
		bits, perUnit    uint64
		wantNum, wantDen uint64
		wantErr          error
	}{
		{50_000_000, 8, 6_250_000, 1, nil},
		{1_000_000, 12_000, 250, 3, nil},
		{44_100 * 16, 16, 44_100, 1, nil},
		{7, 7, 1, 1, nil},
		{0, 8, 0, 0, pll.ErrInvalidRate},
		{1_000_000, 0, 0, 0, pll.ErrInvalidRate},
	}
	for _, tt := range tests {
		r, err := pll.BitsPerSecond(tt.bits, tt.perUnit)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) || r != (pll.Rate{}) {
				t.Errorf("BitsPerSecond(%d, %d) = (%d/%d, %v), want the zero Rate and %v",
					tt.bits, tt.perUnit, r.Num(), r.Den(), err, tt.wantErr)
			}
			continue
		}
		if err != nil || r.Num() != tt.wantNum || r.Den() != tt.wantDen {
			t.Errorf("BitsPerSecond(%d, %d) = (%d/%d, %v), want %d/%d units per second",
				tt.bits, tt.perUnit, r.Num(), r.Den(), err, tt.wantNum, tt.wantDen)
		}
	}
}

func TestRateString(t *testing.T) {
	must := func(num, den uint64) pll.Rate {
		r, err := pll.UnitsPerSecond(num, den)
		if err != nil {
			t.Fatalf("UnitsPerSecond(%d, %d): %v", num, den, err)
		}
		return r
	}
	tests := []struct {
		r    pll.Rate
		want string
	}{
		{must(48000, 1), "48000 units/s"},
		{must(30000, 1001), "30000/1001 units/s"},
		{must(60000, 2002), "30000/1001 units/s"},
		{must(3, 8), "3/8 units/s"},
		{pll.Rate{}, "invalid rate"},
	}
	for _, tt := range tests {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
