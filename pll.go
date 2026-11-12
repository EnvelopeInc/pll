// Package pll provides a deterministic, transport-agnostic virtual-time pacer
// and an optional synchronous scheduler. Scheduling observes actual acceptance
// only through the caller's callback; it does not own transport or guarantee delivery timing.
package pll

import (
	"errors"
	"math/big"
	"time"
)

// MaxBatchSize is the largest batch Advance can return in one call.
const MaxBatchSize uint64 = 1024

var (
	// ErrInvalidConfig indicates that a rate or unit size cannot be paced.
	ErrInvalidConfig = errors.New("pll: invalid configuration")
	// ErrTimeOverflow indicates that a virtual timestamp does not fit time.Duration.
	ErrTimeOverflow = errors.New("pll: virtual time overflow")
	// ErrInvalidNow indicates that Advance received a negative elapsed time.
	ErrInvalidNow = errors.New("pll: now must be non-negative")
	// ErrInvalidBatchSize indicates a batch limit outside [1, MaxBatchSize].
	ErrInvalidBatchSize = errors.New("pll: invalid batch size")
	// ErrUnitIndexOverflow indicates that the sequential emission counter is exhausted.
	ErrUnitIndexOverflow = errors.New("pll: unit index overflow")
)

// Option configures a PLL. No public options are defined yet.
//
// The unexported argument reserves the functional-option seam for options
// provided by this package; callers should not construct Option values.
type Option func(*options) error

type options struct{}

// Emission describes one scheduled unit. Timestamp is virtual time from the
// zero origin, not the time at which a caller actually delivers the unit.
// References require an unmodified whole-value copy from Advance on the same
// PLL. Exported-field serialization does not preserve private provenance.
type Emission struct {
	UnitIndex         uint64
	Slot              uint64
	Timestamp         time.Duration
	owner             *emissionOwner
	assignedSlot      uint64
	assignedUnit      uint64
	assignedTimestamp time.Duration
}
type emissionOwner struct{ marker byte }

// Batch contains the emitted units from one bounded Advance call.
type Batch struct {
	Emissions []Emission
	MoreDue   bool
}

// PLL combines an immutable virtual timebase with deterministic pacing state.
// It owns no units; callers pass the available count on each call and retain
// responsibility for buffering and capacity. Construct it with NewPLL; the
// zero value is not usable. Do not copy a stateful PLL.
type PLL struct {
	clock     timebase
	nextSlot  uint64
	unitCount uint64
	owner     *emissionOwner
	syncClock *correctedClock
}

// timebase keeps rate arithmetic separate from availability and pacing state.
type timebase struct {
	bitsPerUnit uint64
	rate        uint64
}

// NewPLL creates a pacer for rate bits per second and packetSize bytes per
// unit. Both values must be positive. The rational period must be at least 1ns
// and its ceiling-rounded first due deadline must fit in time.Duration.
// Invalid configuration or a nil option returns an
// error. Slot 0 is due at the explicit zero-time origin.
func NewPLL(rate, packetSize int64, opts ...Option) (*PLL, error) {
	if rate <= 0 || packetSize <= 0 {
		return nil, ErrInvalidConfig
	}

	bits := new(big.Int).Mul(big.NewInt(packetSize), big.NewInt(8))
	if !bits.IsUint64() {
		return nil, ErrInvalidConfig
	}
	r := uint64(rate)
	periodNumerator := new(big.Int).Mul(new(big.Int).Set(bits), big.NewInt(1_000_000_000))
	rateValue := new(big.Int).SetUint64(r)
	if periodNumerator.Cmp(rateValue) < 0 {
		// Sub-nanosecond periods cannot map to strictly increasing integer
		// timestamps and are not representable by time.Duration.
		return nil, ErrInvalidConfig
	}
	periodNanos, remainder := new(big.Int), new(big.Int)
	periodNanos.QuoRem(periodNumerator, rateValue, remainder)
	if remainder.Sign() != 0 {
		periodNanos.Add(periodNanos, big.NewInt(1))
	}
	if !periodNanos.IsInt64() {
		return nil, ErrInvalidConfig
	}

	cfg := &options{}
	for _, opt := range opts {
		if opt == nil {
			return nil, ErrInvalidConfig
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	return &PLL{clock: timebase{bitsPerUnit: bits.Uint64(), rate: r}, owner: &emissionOwner{}}, nil
}

// VirtualTime returns floor(slot * packetSize * 8 * 1e9 / rate) nanoseconds
// from the zero origin. Each timestamp is calculated from the absolute slot
// index, so fractional periods do not accumulate rounding drift. It does not
// depend on availability or pacing state.
func (p *PLL) VirtualTime(slot uint64) (time.Duration, error) {
	return p.clock.timestamp(slot)
}

// ScheduledTime returns the current prospective corrected schedule. Unlike
// VirtualTime, it changes as synchronization updates the unassigned clock.
// Slots before the current correction segment return ErrHistoricalSchedule;
// retain Emission.Timestamp for historical assignments.
func (p *PLL) ScheduledTime(slot uint64) (time.Duration, error) {
	if p.syncClock == nil {
		return p.clock.timestamp(slot)
	}
	return p.syncClock.timestamp(slot)
}

func (t timebase) timestamp(slot uint64) (time.Duration, error) {
	n := new(big.Int).Mul(new(big.Int).SetUint64(slot), new(big.Int).SetUint64(t.bitsPerUnit))
	n.Mul(n, big.NewInt(1_000_000_000))
	n.Quo(n, new(big.Int).SetUint64(t.rate))
	if !n.IsInt64() {
		return 0, ErrTimeOverflow
	}
	return time.Duration(n.Int64()), nil
}

func (t timebase) latestDue(now time.Duration) (uint64, error) {
	// floor(now_ns * rate / (bits_per_unit * 1e9))
	n := new(big.Int).Mul(big.NewInt(int64(now)), new(big.Int).SetUint64(t.rate))
	d := new(big.Int).Mul(new(big.Int).SetUint64(t.bitsPerUnit), big.NewInt(1_000_000_000))
	slot := n.Quo(n, d)
	if !slot.IsUint64() {
		return 0, ErrTimeOverflow
	}
	return slot.Uint64(), nil
}

// Advance emits due slots using the caller's availability snapshot. The limit
// must be between 1 and MaxBatchSize. The caller consumes exactly the returned
// number of units; payloads remain caller-owned. Advance mutates state and is
// not safe for concurrent calls on the same PLL. Due eligibility uses exact
// rational time, not the floor-rounded timestamp; decreasing now never rewinds
// assigned or skipped slots.
func (p *PLL) Advance(now time.Duration, availableUnits, maxBatch uint64) (Batch, error) {
	if now < 0 {
		return Batch{}, ErrInvalidNow
	}
	if maxBatch == 0 || maxBatch > MaxBatchSize {
		return Batch{}, ErrInvalidBatchSize
	}
	latest, err := p.latestDue(now)
	if err != nil {
		return Batch{}, err
	}
	if p.syncClock != nil {
		if p.nextSlot < p.syncClock.start {
			return Batch{}, ErrHistoricalSchedule
		}
		if p.syncClock.exact(p.nextSlot).Cmp(new(big.Rat).SetInt64(int64(now))) > 0 {
			return Batch{Emissions: []Emission{}}, nil
		}
	}
	if latest == ^uint64(0) && latest >= p.nextSlot {
		return Batch{}, ErrTimeOverflow
	}
	if latest < p.nextSlot || availableUnits == 0 {
		if availableUnits == 0 && latest >= p.nextSlot {
			p.nextSlot = latest + 1
		}
		return Batch{Emissions: []Emission{}}, nil
	}
	start := p.nextSlot
	due := latest - start + 1
	if availableUnits < due {
		start += due - availableUnits
	}
	count := latest - start + 1
	emitCount := count
	if emitCount > availableUnits {
		emitCount = availableUnits
	}
	if emitCount > maxBatch {
		emitCount = maxBatch
	}
	if emitCount > ^uint64(0)-p.unitCount {
		return Batch{}, ErrUnitIndexOverflow
	}
	batch := Batch{Emissions: make([]Emission, 0, emitCount)}
	for i := uint64(0); i < emitCount; i++ {
		slot := start + i
		timestamp, err := p.ScheduledTime(slot)
		if err != nil {
			return Batch{}, err
		}
		unit := p.unitCount + i
		emission := Emission{UnitIndex: unit, Slot: slot, Timestamp: timestamp, owner: p.owner, assignedSlot: slot, assignedUnit: unit, assignedTimestamp: timestamp}
		batch.Emissions = append(batch.Emissions, emission)
	}
	remainingEligible := count - emitCount
	batch.MoreDue = remainingEligible > 0 && availableUnits > emitCount
	if batch.MoreDue {
		p.nextSlot = start + emitCount
	} else {
		p.nextSlot = latest + 1
	}
	p.unitCount += emitCount
	return batch, nil
}
