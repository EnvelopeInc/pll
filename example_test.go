package pll_test

import (
	"fmt"
	"log"
	"time"

	"github.com/EnvelopeInc/pll"
)

func ExamplePLL_Advance() {
	p, err := pll.NewPLL(19_392_000, 188) // bits per second, bytes per unit
	if err != nil {
		log.Fatal(err)
	}

	now := time.Duration(0)               // elapsed time from the caller's chosen zero origin
	batch, err := p.Advance(now, 1, 1024) // one unit available, bounded batch
	if err != nil {
		log.Fatal(err)
	}
	for _, emission := range batch.Emissions {
		fmt.Printf("unit %d, slot %d has virtual time %s\n", emission.UnitIndex, emission.Slot, emission.Timestamp)
		// The caller consumes exactly len(batch.Emissions) units.
	}
	// Output: unit 0, slot 0 has virtual time 0s
}
