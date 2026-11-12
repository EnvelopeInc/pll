package pll_test

import (
	"fmt"
	"time"

	"github.com/version2beta/pll"
)

// Pace audio samples at 48 kHz: wake on any cadence and write what came due.
func ExamplePacer_Due() {
	rate, _ := pll.UnitsPerSecond(48_000, 1)
	p, _ := pll.NewPacer(rate)

	for _, now := range []time.Duration{0, 10 * time.Millisecond, 25 * time.Millisecond} {
		span, _ := p.Due(now, 4096)
		fmt.Printf("at %v: %d samples, slots %d-%d\n", now, span.Count, span.First, span.First+span.Count-1)
	}
	// Output:
	// at 0s: 1 samples, slots 0-0
	// at 10ms: 480 samples, slots 1-480
	// at 25ms: 720 samples, slots 481-1200
}

// Send variable-size datagrams at 50 Mbit/s: one slot per byte.
func ExamplePacer_Claim() {
	rate, _ := pll.BitsPerSecond(50_000_000, 8)
	p, _ := pll.NewPacer(rate)

	for _, size := range []uint64{1500, 400, 1500} {
		span, _ := p.Claim(size)
		fmt.Printf("%4d bytes: send at %v\n", size, span.Time(0))
	}
	// Output:
	// 1500 bytes: send at 0s
	//  400 bytes: send at 240µs
	// 1500 bytes: send at 304µs
}

// A rate that is not a whole number of units per second stays exact forever.
func ExampleSpan_All() {
	rate, _ := pll.UnitsPerSecond(30000, 1001)
	p, _ := pll.NewPacer(rate)
	span, _ := p.Claim(4)
	for slot, at := range span.All() {
		fmt.Println(slot, at)
	}
	fmt.Println(rate)
	// Output:
	// 0 0s
	// 1 33.366666ms
	// 2 66.733333ms
	// 3 100.1ms
	// 30000/1001 units/s
}

// Follow a device whose clock runs 50 ppm fast.
func ExamplePacer_SetTrim() {
	rate, _ := pll.UnitsPerSecond(1000, 1)
	p, _ := pll.NewPacer(rate)
	p.Claim(1000)
	_ = p.SetTrim(50_000_000) // 50 ppm = 50,000,000 parts per trillion
	span, _ := p.Claim(1001)
	fmt.Println(span.Time(1000))
	// Output:
	// 1.999950002s
}
