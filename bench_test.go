package pll_test

import (
	"testing"
	"time"

	"github.com/version2beta/pll"
)

func benchPacer(b *testing.B, num, den uint64, trim int64) *pll.Pacer {
	b.Helper()
	r, err := pll.UnitsPerSecond(num, den)
	if err != nil {
		b.Fatal(err)
	}
	p, err := pll.NewPacer(r)
	if err != nil {
		b.Fatal(err)
	}
	if err := p.SetTrim(trim); err != nil {
		b.Fatal(err)
	}
	return p
}

// BenchmarkDueOne hands out one slot per call: the per-unit cost.
func BenchmarkDueOne(b *testing.B) {
	p := benchPacer(b, 6_250_000, 1, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s, _ := p.Due(time.Duration(i)*160, 1)
		if s.Count > 0 {
			_ = s.Time(0)
		}
	}
}

// BenchmarkDueBatch hands out 64 slots per call and reads every time.
func BenchmarkDueBatch(b *testing.B) {
	p := benchPacer(b, 6_250_000, 1, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s, _ := p.Due(time.Duration(i)*64*160, 64)
		for j := uint64(0); j < s.Count; j++ {
			_ = s.Time(j)
		}
	}
}

// BenchmarkDueTrimmed is BenchmarkDueOne with a 50 ppm trim.
func BenchmarkDueTrimmed(b *testing.B) {
	p := benchPacer(b, 6_250_000, 1, 50_000_000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s, _ := p.Due(time.Duration(i)*160, 1)
		if s.Count > 0 {
			_ = s.Time(0)
		}
	}
}

// BenchmarkClaim claims 1500-byte runs at a byte rate.
func BenchmarkClaim(b *testing.B) {
	p := benchPacer(b, 6_250_000, 1, 0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s, err := p.Claim(1500)
		if err != nil {
			b.Fatal(err)
		}
		_ = s.Time(0)
	}
}
