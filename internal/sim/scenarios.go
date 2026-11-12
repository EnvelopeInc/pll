// scenarios.go is complete; do not change it.

package sim

import (
	"time"

	"github.com/version2beta/pll"
)

const (
	ms = time.Millisecond
	us = time.Microsecond
	s  = time.Second
)

// Scenarios returns the follower scenarios, in a fixed order.
func Scenarios() []Scenario {
	return []Scenario{
		{
			Name:     "sound-card",
			Clock:    Clock{Nominal: 48000, PPM: 80},
			Link:     Link{Interval: 10 * ms, Jitter: 20 * us},
			Duration: 120 * s, Settle: 60 * s,
		},
		{
			Name:  "sensor-network",
			Clock: Clock{Nominal: 1_000_000, Offset: 3 * s, PPM: -150},
			Link: Link{Interval: 100 * ms, Base: 1 * ms, Jitter: 2 * ms,
				SpikeProb: 0.02, SpikeMean: 50 * ms, Batch: 8},
			Duration: 600 * s, Settle: 300 * s,
			Config: NetworkConfig,
		},
		{
			Name:     "rate-change",
			Clock:    Clock{Nominal: 48000, Changes: []Change{{At: 60 * s, PPM: 100}}},
			Link:     Link{Interval: 10 * ms, Jitter: 20 * us},
			Duration: 180 * s, Settle: 120 * s,
		},
		{
			Name:     "outage",
			Clock:    Clock{Nominal: 48000, PPM: 30},
			Link:     Link{Interval: 10 * ms, Jitter: 20 * us, Outages: []Span{{From: 60 * s, To: 90 * s}}},
			Duration: 150 * s, Settle: 90 * s,
		},
		{
			Name:     "clock-step",
			Clock:    Clock{Nominal: 48000, PPM: 20, Steps: []Step{{At: 60 * s, Jump: 2 * s}}},
			Link:     Link{Interval: 10 * ms, Jitter: 20 * us},
			Duration: 120 * s, Settle: 70 * s,
		},
		{
			Name:     "aging-crystal",
			Clock:    Clock{Nominal: 48000, PPM: 10, DriftPPMPerHour: 1},
			Link:     Link{Interval: 10 * ms, Jitter: 20 * us},
			Duration: 2 * time.Hour, Settle: 10 * time.Minute,
		},
	}
}

// NetworkConfig is the follower setting the network scenario uses: a 1 s
// lower-envelope window, a 5 mHz loop and lock thresholds sized for
// millisecond jitter.
func NetworkConfig(c pll.FollowerConfig) pll.FollowerConfig {
	c.Window = s
	c.Bandwidth = 0.005
	c.LockThreshold = 2 * ms
	c.OutlierThreshold = 20 * ms
	return c
}

// DeviceSink is the device-sink scenario: a device whose clock runs 40 ppm
// fast drains a buffer the pacer fills.
func DeviceSink(link bool) Sink {
	return Sink{
		Nominal: 48000, PPM: 40, Lead: 100 * ms,
		Wake: 10 * ms, Observe: 100 * ms,
		Duration: 600 * s, Settle: 300 * s, Link: link,
	}
}
