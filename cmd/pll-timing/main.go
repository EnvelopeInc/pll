// Command pll-timing prints a bounded deterministic virtual-time schedule sample.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/EnvelopeInc/pll"
)

const maxEmissions = 1_000_000

func main() {
	rate := flag.Int64("rate", 19_392_000, "target bitrate in exact bits per second")
	unitBytes := flag.Int64("unit-bytes", 188, "unit size in bytes")
	count := flag.Uint64("count", 10, "number of virtual timestamps to print (1 to 1,000,000)")
	flag.Parse()

	if *count == 0 || *count > maxEmissions {
		fatal("-count must be between 1 and 1,000,000")
	}
	p, err := pll.NewPLL(*rate, *unitBytes)
	if err != nil {
		fatal(err.Error())
	}
	for slot := uint64(0); slot < *count; slot++ {
		timestamp, err := p.VirtualTime(slot)
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("slot=%d virtual=%s\n", slot, timestamp)
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "pll-timing:", message)
	os.Exit(2)
}
