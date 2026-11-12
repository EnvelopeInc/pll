package pll_test

import (
	"fmt"
	"time"

	"github.com/version2beta/pll"
)

// Put a sensor's readings on the local timeline. The sensor counts
// microseconds on its own crystal, which runs 50 ppm fast.
func ExampleFollower() {
	nominal, _ := pll.UnitsPerSecond(1_000_000, 1)
	f, _ := pll.NewFollower(pll.DefaultFollowerConfig(nominal))

	sensor := func(local time.Duration) int64 { // the sensor's counter at local time
		return int64(local.Seconds() * 1e6 * (1 + 50e-6))
	}
	for local := time.Duration(0); local <= time.Minute; local += 10 * time.Millisecond {
		f.Observe(local, sensor(local))
	}
	when, _ := f.Local(sensor(time.Minute + 500*time.Millisecond))
	fmt.Printf("rate offset %.1f ppm, %v\n", f.Offset()*1e6, f.State(time.Minute))
	fmt.Println("a reading taken at 60.5 s maps to", when.Round(time.Microsecond))
	// Output:
	// rate offset 50.0 ppm, locked
	// a reading taken at 60.5 s maps to 1m0.5s
}

// Keep a device with its own clock fed: the units it has consumed are a
// reading of its clock, and the follower's trim makes the pacer keep up.
func ExampleFollower_Trim() {
	rate, _ := pll.UnitsPerSecond(48000, 1)
	p, _ := pll.NewPacer(rate)
	f, _ := pll.NewFollower(pll.DefaultFollowerConfig(rate))

	consumed := func(t time.Duration) int64 { // the device runs 40 ppm fast
		return int64(t.Seconds() * 48000 * (1 + 40e-6))
	}
	for t := time.Duration(0); t <= 5*time.Minute; t += 100 * time.Millisecond {
		f.Observe(t, consumed(t))
		_ = p.SetTrim(f.Trim())
	}
	fmt.Printf("pacer trim %.0f ppm\n", float64(p.Trim())/1e6)
	// Output:
	// pacer trim 40 ppm
}
