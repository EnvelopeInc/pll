// Command pll-timing characterizes TriggerCh receive timing on the current host.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/EnvelopeInc/pll"
)

const (
	maxDuration = 30 * time.Second
	maxSamples  = 1_000_000
)

func main() {
	rate := flag.Float64("mbps", 0, "target bitrate in decimal Mbps (required)")
	duration := flag.Duration("duration", 5*time.Second, "measurement duration (100ms to 30s)")
	flag.Parse()
	if *rate <= 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0) {
		fatal("-mbps must be finite and positive")
	}
	if *duration < 100*time.Millisecond || *duration > maxDuration {
		fatal("-duration must be between 100ms and 30s")
	}

	const packetBits = 188 * 8
	periodNanos := float64(packetBits) / (*rate * 1e6) * 1e9
	if math.IsInf(periodNanos, 0) || periodNanos < 1 || periodNanos >= float64(math.MaxInt64) {
		fatal("-mbps produces a period outside the valid time.Timer range")
	}
	period := time.Duration(periodNanos)
	pll := pll.NewPLL(*rate, 0, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	pll.StartContext(ctx)

	timestamps := make([]time.Time, 0, 4096)
	started := time.Now()
	timer := time.NewTimer(*duration)
	defer timer.Stop()
	truncated := false
	collecting := true
	for collecting {
		select {
		case _, ok := <-pll.TriggerCh:
			if !ok {
				collecting = false
				break
			}
			timestamps = append(timestamps, time.Now())
			if len(timestamps) == maxSamples {
				truncated = true
				collecting = false
			}
		case <-timer.C:
			collecting = false
		}
	}
	elapsed := time.Since(started)
	cancel()
	pll.Stop() // Wait for the worker, including any blocked TriggerCh send, to exit.

	printReport(*rate, period, elapsed, timestamps, truncated)
}

func printReport(targetMbps float64, expected time.Duration, elapsed time.Duration, timestamps []time.Time, truncated bool) {
	fmt.Println("PLL TriggerCh channel-receive timing characterization (current OS/hardware only)")
	fmt.Println("Not wire-level proof and not a cross-platform timing guarantee.")
	fmt.Printf("target: %.6g Mbps | expected period: %s | elapsed: %s | samples: %d", targetMbps, expected, elapsed.Round(time.Microsecond), len(timestamps))
	if truncated {
		fmt.Print(" (sample limit reached; stopped early)")
	}
	fmt.Println()
	if len(timestamps) < 2 {
		fmt.Println("insufficient samples for period and jitter summaries")
		return
	}

	intervals := make([]time.Duration, 0, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		intervals = append(intervals, timestamps[i].Sub(timestamps[i-1]))
	}
	observed := time.Duration(timestamps[len(timestamps)-1].Sub(timestamps[0]).Nanoseconds() / int64(len(intervals)))
	if observed <= 0 {
		fmt.Println("could not compute a positive observed period")
		return
	}
	observedMbps := float64(188*8) / observed.Seconds() / 1e6
	fmt.Printf("observed average period: %s | average-rate error: %+.4f%%\n", observed, (observedMbps/targetMbps-1)*100)

	jitters := make([]time.Duration, len(intervals))
	for i, interval := range intervals {
		jitters[i] = interval - observed
		if jitters[i] < 0 {
			jitters[i] = -jitters[i]
		}
	}
	sort.Slice(jitters, func(i, j int) bool { return jitters[i] < jitters[j] })
	fmt.Printf("interval jitter (absolute deviation from observed mean): min %s | median %s | p95 %s | p99 %s | max %s\n",
		jitters[0], percentile(jitters, .50), percentile(jitters, .95), percentile(jitters, .99), jitters[len(jitters)-1])
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "pll-timing:", message)
	os.Exit(2)
}
