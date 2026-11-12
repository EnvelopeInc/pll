package pll

import (
	"math"
	"testing"
	"time"
)

func TestSynchronizerQuantizationStaysWithinClampAndSlew(t *testing.T) {
	for _, tc := range []struct {
		name        string
		phase       time.Duration
		clamp, slew float64
	}{
		{"positive-subgrid-clamp", 1, .0000006, 1000},
		{"negative-subgrid-clamp", -1, .0000006, 1000},
		{"positive-offgrid-clamp", 1, .0000016, 1000},
		{"negative-offgrid-clamp", -1, .0000016, 1000},
		{"positive-tiny-slew", 1, 1000, .0000006},
		{"negative-tiny-slew", -1, 1000, .0000006},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := NewPLL(8_000_000, 1)
			s := testSync(t, p)
			s.cfg.Kp, s.cfg.Ki = 1, 1
			s.cfg.MaxCorrectionPPM, s.cfg.SlewLimitPPMPerSecond = tc.clamp, tc.slew
			first := emissionAt(t, p, 0)
			if err := s.Observe(0, ReferenceSample{Emission: first}); err != nil {
				t.Fatal(err)
			}
			for n := 1; n <= 3; n++ {
				e := emissionAt(t, p, time.Duration(n)*time.Second)
				ref := e.Timestamp + tc.phase
				delta := ref - s.lastReference
				before := s.Trim()
				if err := s.Observe(time.Duration(n)*time.Second, ReferenceSample{Emission: e, ReferenceTime: ref, ObservedAt: time.Duration(n) * time.Second}); err != nil {
					t.Fatal(err)
				}
				if math.Abs(s.Trim()) > tc.clamp/1e6 {
					t.Fatalf("trim=%g exceeds clamp=%g", s.Trim(), tc.clamp/1e6)
				}
				if math.Abs(s.Trim()-before) > tc.slew/1e6*delta.Seconds() {
					t.Fatalf("trim step=%g exceeds slew=%g", math.Abs(s.Trim()-before), tc.slew/1e6*delta.Seconds())
				}
			}
		})
	}
}

func TestAbsCountDifferenceDoesNotUnderflow(t *testing.T) {
	if absCountDifference(999, 1000) != 1 || absCountDifference(1000, 999) != 1 {
		t.Fatal("unsigned count difference underflowed")
	}
}
