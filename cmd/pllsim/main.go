// main.go is complete; do not change it.

// Command pllsim runs the simulator scenarios against the Follower and prints
// what each one measured. Every run is deterministic: the same seed gives the
// same table on every machine.
//
//	go run ./cmd/pllsim [-seed 1]
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/version2beta/pll"
	"github.com/version2beta/pll/internal/sim"
)

func main() {
	seed := flag.Uint64("seed", 1, "random seed for jitter and delays")
	flag.Parse()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "scenario\tlocked at\tRMS error\tmax error\trate error\tsteps\trejected\tend state\t")
	for _, sc := range sim.Scenarios() {
		r, err := sim.Run(sc, *seed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pllsim: %s: %v\n", sc.Name, err)
			os.Exit(1)
		}
		lock := "never"
		if r.LockTime >= 0 {
			lock = r.LockTime.Round(time.Millisecond).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%.3f ppm\t%d\t%d\t%v\t\n", sc.Name, lock,
			r.RMSError.Round(100*time.Nanosecond), r.MaxError.Round(100*time.Nanosecond),
			r.MaxRatePPM, r.Stepped, r.Rejected, r.EndState)
	}
	w.Flush()
	fmt.Println()

	for _, link := range []bool{true, false} {
		r, err := sim.RunSink(sim.DeviceSink(link))
		if err != nil {
			fmt.Fprintf(os.Stderr, "pllsim: device-sink: %v\n", err)
			os.Exit(1)
		}
		how := "linked  "
		if !link {
			how = "unlinked"
		}
		fmt.Printf("device-sink %s queue %d to %d units, %d underruns, pacer trim %.3f ppm\n",
			how, r.MinQueue, r.MaxQueue, r.Underruns, float64(r.EndTrim)/1e6)
	}

	rate, _ := pll.UnitsPerSecond(48000, 1)
	lr, err := sim.RunLateWakeups(rate, 10*time.Millisecond, 20*time.Millisecond, time.Minute, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pllsim: late-wakeups: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("late-wakeups: %d of %d due slots handed out, %d gaps, %d early\n",
		lr.HandedOut, lr.Expected, lr.Gaps, lr.Early)
}
