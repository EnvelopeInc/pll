package pll_test

// Proofs that the Pacer is exact: boundary cases at the numeric limits, a
// seeded randomized differential test against the naive model in
// model_test.go, a native fuzz target, and zero-allocation checks.

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/version2beta/pll"
)

// TestBoundaries runs short operation sequences at the extremes and compares
// each result with the model.
func TestBoundaries(t *testing.T) {
	const maxU = math.MaxUint64
	const maxD = math.MaxInt64
	const maxTrim = trimScale - 1
	tests := []struct {
		name     string
		num, den uint64
		ops      []op
	}{
		{"fastest rate", maxU, 1, []op{
			{kind: opNextDue}, {kind: opDue, now: 1, n: 1000}, {kind: opDue, now: maxD, n: maxU},
			{kind: opNextDue}, {kind: opClaim, n: 1}, {kind: opSkip, now: maxD},
		}},
		{"fastest rate, trimmed both ways", maxU, 1, []op{
			{kind: opSetTrim, trim: maxTrim}, {kind: opDue, now: 7, n: maxU},
			{kind: opSetTrim, trim: -maxTrim}, {kind: opDue, now: 1_000_000, n: 3}, {kind: opNextDue},
			{kind: opClaim, n: 1 << 40}, {kind: opNextDue},
		}},
		{"slowest rate", 1, maxU, []op{
			{kind: opNextDue}, {kind: opClaim, n: 1}, {kind: opNextDue}, {kind: opClaim, n: 1},
			{kind: opDue, now: maxD, n: 5}, {kind: opSetTrim, trim: maxTrim},
		}},
		{"near one unit per second with huge terms", maxU, maxU - 1, []op{
			{kind: opClaim, n: 9_223_372_036}, {kind: opNextDue}, {kind: opClaim, n: 1},
			{kind: opDue, now: maxD, n: maxU}, {kind: opSetTrim, trim: -1},
		}},
		{"one unit per second to the last representable time", 1, 1, []op{
			{kind: opClaim, n: 9_223_372_036}, {kind: opNextDue}, {kind: opClaim, n: 1},
			{kind: opNextDue}, {kind: opDue, now: maxD, n: 1}, {kind: opSetTrim, trim: 1},
		}},
		{"large odd terms", 1 << 63, 3, []op{
			{kind: opDue, now: 2_000_000_000, n: maxU}, {kind: opNextDue}, {kind: opSetTrim, trim: 12345},
			{kind: opDue, now: 4_000_000_000, n: maxU}, {kind: opClaim, n: 1},
		}},
		{"tiny trim steps on a fractional rate", 30000, 1001, []op{
			{kind: opClaim, n: 1}, {kind: opSetTrim, trim: 1}, {kind: opClaim, n: 999},
			{kind: opSetTrim, trim: -1}, {kind: opClaim, n: 1_000_000}, {kind: opSetTrim, trim: maxTrim},
			{kind: opClaim, n: 1_000_000}, {kind: opSetTrim, trim: -maxTrim}, {kind: opClaim, n: 3},
			{kind: opNextDue}, {kind: opDue, now: maxD, n: 10},
		}},
		{"a period of many years after the slowest trim", 48000, 1, []op{
			{kind: opSetTrim, trim: -maxTrim}, {kind: opClaim, n: 400}, {kind: opNextDue},
			{kind: opClaim, n: 100}, {kind: opDue, now: maxD, n: maxU},
		}},
		{"slots near the top of the index range", 1 << 63, 1, []op{
			{kind: opDue, now: 1_999_999_999, n: maxU}, {kind: opNextDue}, {kind: opDue, now: 2_000_000_000, n: maxU},
			{kind: opNextDue}, {kind: opDue, now: maxD, n: 2}, {kind: opClaim, n: 1},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runOps(t, tt.num, tt.den, tt.ops) })
	}
}

// rateCases mixes everyday rates with ones chosen to stress the arithmetic.
var rateCases = [][2]uint64{
	{48000, 1}, {44100, 1}, {30000, 1001}, {3, 8}, {1000, 1}, {6_250_000, 1},
	{300_000_000, 1}, {2_000_000_000, 1}, {1, 1}, {7, 1 << 40},
	{math.MaxUint64, 1}, {1, math.MaxUint64}, {math.MaxUint64, math.MaxUint64 - 1}, {1 << 63, 3},
}

var trimCases = []int64{
	0, 1, -1, 50_000_000, -50_000_000, 1_000_000_000, -1_000_000_000,
	trimScale - 1, -(trimScale - 1), trimScale, -trimScale, math.MinInt64,
}

// randomOps builds a plausible but adversarial sequence: time mostly moves
// forward by amounts scaled to the period, sometimes jumps back, to the exact
// next due time, or to the limits.
func randomOps(rng *rand.Rand, num, den uint64, count int) []op {
	period := float64(den) * 1e9 / float64(num)
	if period < 1e-9 {
		period = 1e-9
	}
	var now int64
	ops := make([]op, 0, count)
	pickN := func() uint64 {
		switch rng.IntN(6) {
		case 0:
			return 1
		case 1:
			return uint64(rng.IntN(8) + 1)
		case 2:
			return uint64(rng.IntN(5000) + 1)
		case 3:
			return math.MaxUint64
		case 4:
			return rng.Uint64()
		default:
			return 0
		}
	}
	for len(ops) < count {
		switch rng.IntN(10) {
		case 0, 1, 2:
			step := period * float64(rng.IntN(64)) * rng.Float64()
			if step > float64(math.MaxInt64/4) {
				step = float64(math.MaxInt64 / 4)
			}
			now += int64(step)
		case 3:
			now -= int64(rng.IntN(1000))
		case 4:
			now = rng.Int64()
		case 5:
			now = math.MaxInt64
		case 6:
			now = -1
		}
		if now < -1 || now > math.MaxInt64-1 {
			now = math.MaxInt64
		}
		o := op{now: now}
		switch rng.IntN(int(numOpKinds) + 2) {
		case 0, 1:
			o.kind, o.n = opDue, pickN()
		case 2:
			o.kind, o.n = opClaim, pickN()
		case 3:
			o.kind, o.n = opSkip, pickN()
		case 4, 5:
			o.kind = opNextDue
		default:
			o.kind = opSetTrim
			if rng.IntN(3) == 0 {
				o.trim = rng.Int64N(2*trimScale) - trimScale
			} else {
				o.trim = trimCases[rng.IntN(len(trimCases))]
			}
		}
		ops = append(ops, o)
	}
	return ops
}

func TestDifferential(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261111, 1))
	for i := 0; i < 400; i++ {
		var num, den uint64
		if i < len(rateCases)*10 {
			num, den = rateCases[i%len(rateCases)][0], rateCases[i%len(rateCases)][1]
		} else {
			num = rng.Uint64() >> rng.IntN(64)
			den = rng.Uint64() >> rng.IntN(64)
			if num == 0 {
				num = 1
			}
			if den == 0 {
				den = 1
			}
		}
		ops := randomOps(rng, num, den, 60)
		runOps(t, num, den, ops)
		if t.Failed() {
			return
		}
	}
}

// FuzzPacer decodes a rate and an operation sequence from bytes. Run it with
// go test -run '^$' -fuzz '^FuzzPacer$' -fuzztime 30s .
func FuzzPacer(f *testing.F) {
	seed := func(num, den uint64, rest ...byte) []byte {
		b := binary.LittleEndian.AppendUint64(nil, num)
		b = binary.LittleEndian.AppendUint64(b, den)
		return append(b, rest...)
	}
	f.Add(seed(48000, 1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9))
	f.Add(seed(30000, 1001, 4, 9, 9, 9, 9, 9, 9, 9, 9, 0, 200, 1, 0, 0, 0, 0, 0, 0, 0))
	f.Add(seed(math.MaxUint64, 1, 0, 255, 255, 255, 255, 255, 255, 255, 127, 255, 255, 255, 255, 255, 255, 255, 255))
	f.Add(seed(1, math.MaxUint64, 1, 3, 0, 0, 0, 0, 0, 0, 0, 3))
	f.Add(seed(3, 8, 4, 1, 0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 3, 2))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 16 {
			return
		}
		num := binary.LittleEndian.Uint64(data)
		den := binary.LittleEndian.Uint64(data[8:])
		if num == 0 {
			num = 1
		}
		if den == 0 {
			den = 1
		}
		data = data[16:]
		next := func() uint64 {
			var buf [8]byte
			n := copy(buf[:], data)
			data = data[n:]
			return binary.LittleEndian.Uint64(buf[:])
		}
		var ops []op
		for len(data) > 0 && len(ops) < 48 {
			k := data[0]
			data = data[1:]
			o := op{kind: opKind(k % byte(numOpKinds))}
			v := next()
			switch o.kind {
			case opDue, opSkip:
				if k&0x80 != 0 {
					o.now = int64(v) // anything, including negative
				} else {
					o.now = int64(v % (1 << 44))
				}
				o.n = next()
				if k&0x40 == 0 {
					o.n %= 4096
				}
			case opClaim:
				o.n = v
				if k&0x40 == 0 {
					o.n %= 4096
				}
			case opSetTrim:
				o.trim = int64(v)
				if k&0x40 == 0 {
					o.trim %= trimScale
				}
			}
			ops = append(ops, o)
		}
		runOps(t, num, den, ops)
	})
}

// TestAllocs checks that the hot paths allocate nothing for everyday rates,
// with and without a trim. It first checks that the pacer actually works, so a
// do-nothing implementation cannot pass.
func TestAllocs(t *testing.T) {
	for _, rc := range [][2]uint64{{48000, 1}, {30000, 1001}, {6_250_000, 1}} {
		for _, trim := range []int64{0, 50_000_000, -50_000_000} {
			p := mustPacer(t, rc[0], rc[1])
			if err := p.SetTrim(trim); err != nil {
				t.Fatalf("SetTrim(%d): %v", trim, err)
			}
			m := newModel(rc[0], rc[1])
			m.setTrim(trim)
			want, _ := m.due(int64(time.Second), 1<<20)
			got, err := p.Due(time.Second, 1<<20)
			if err != nil || got.Count != want.count || got.Count == 0 {
				t.Fatalf("%d/%d units/s, trim %d: Due(1s) handed out %d slots (%v), want %d",
					rc[0], rc[1], trim, got.Count, err, want.count)
			}
			if got.Time(got.Count-1) > time.Second {
				t.Fatalf("%d/%d units/s, trim %d: last due slot at %v, after now", rc[0], rc[1], trim, got.Time(got.Count-1))
			}

			now := time.Second
			step := time.Duration(float64(rc[1]) * 1e9 / float64(rc[0]) * 3)
			var sink time.Duration
			allocs := testing.AllocsPerRun(200, func() {
				now += step
				s, _ := p.Due(now, 64)
				if s.Count > 0 {
					sink += s.Time(s.Count - 1)
				}
				_, at, _ := p.NextDue()
				sink += at
				c, _ := p.Claim(2)
				sink += c.Time(1)
				n, _ := p.Skip(now+step, 1)
				sink += time.Duration(n)
			})
			if allocs != 0 {
				t.Errorf("%d/%d units/s, trim %d: %v allocations per call cycle, want 0", rc[0], rc[1], trim, allocs)
			}
			_ = sink
		}
	}
}

var _ = pll.ErrOverflow
