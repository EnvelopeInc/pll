package pll

import (
	"errors"
	"math"
	"math/big"
	"time"
)

var (
	ErrInvalidSyncConfig  = errors.New("pll: invalid synchronization configuration")
	ErrInvalidReference   = errors.New("pll: invalid reference sample")
	ErrStaleReference     = errors.New("pll: stale reference sample")
	ErrReferenceOrder     = errors.New("pll: duplicate or out-of-order reference sample")
	ErrUnassignedEmission = errors.New("pll: reference emission has no assigned slot")
	ErrInvalidEmission    = errors.New("pll: invalid or foreign emission")
	ErrEpochChange        = errors.New("pll: reference epoch changed; reset required")
	ErrPhaseResidual      = errors.New("pll: phase residual exceeds configured limit")
	ErrHistoricalSchedule = errors.New("pll: corrected schedule unavailable for historical slot")
)

// SyncConfig selects an absolute known epoch or relative first-sample anchoring.
// With ReferenceTimeAtVirtualZero, absolute phase error is measurable. With
// FirstSampleAnchoring, the first reference defines the epoch and initial phase
// is unobservable; subsequent relative frequency error remains measurable.
type SyncConfig struct {
	ReferenceTimeAtVirtualZero *time.Duration
	FirstSampleAnchoring       bool
	Kp, Ki                     float64 // Kp: s^-1; Ki: s^-2
	MaxCorrectionPPM           float64
	SlewLimitPPMPerSecond      float64
	MaxSampleAge               time.Duration
	MaxReferenceInterval       time.Duration
	MaxPhaseResidual           time.Duration
	LockTolerance              time.Duration
	LockSamples                uint32
}

// ReferenceSample associates an external reference with an immutable library
// emission snapshot. Preserve the whole Emission value in memory; decoding
// exported-field serialization loses the provenance required by Observe.
// ObservedAt and now are used only for age validation and observation ordering.
type ReferenceSample struct {
	Emission      Emission
	ReferenceTime time.Duration
	ObservedAt    time.Duration
	Epoch         uint64
}

type SyncState uint8

const (
	SyncAcquiring SyncState = iota
	SyncLocked
	SyncHoldover
	SyncReacquiring
)

const correctionDenominator int64 = 1_000_000_000_000

type correctedClock struct {
	start  uint64
	anchor *big.Rat // exact time at start, in nanoseconds
	period *big.Rat // exact corrected period in nanoseconds
	q      int64
}

func (c *correctedClock) exact(slot uint64) *big.Rat {
	d := new(big.Int).SetUint64(slot - c.start)
	return new(big.Rat).Add(c.anchor, new(big.Rat).Mul(new(big.Rat).SetInt(d), c.period))
}
func (c *correctedClock) timestamp(slot uint64) (time.Duration, error) {
	if slot < c.start {
		return 0, ErrHistoricalSchedule
	}
	x := c.exact(slot)
	n := new(big.Int).Quo(x.Num(), x.Denom())
	if !n.IsInt64() {
		return 0, ErrTimeOverflow
	}
	return time.Duration(n.Int64()), nil
}
func (p *PLL) latestDue(now time.Duration) (uint64, error) {
	if p.syncClock == nil {
		return p.clock.latestDue(now)
	}
	c := p.syncClock
	delta := new(big.Rat).Sub(new(big.Rat).SetInt64(int64(now)), c.anchor)
	if delta.Sign() < 0 {
		if c.start == 0 {
			return 0, nil
		}
		return c.start - 1, nil
	}
	offset := new(big.Rat).Quo(delta, c.period)
	whole := new(big.Int).Quo(offset.Num(), offset.Denom())
	if !whole.IsUint64() {
		return 0, ErrTimeOverflow
	}
	n := new(big.Int).Add(new(big.Int).SetUint64(c.start), whole)
	if !n.IsUint64() {
		return 0, ErrTimeOverflow
	}
	return n.Uint64(), nil
}

// Synchronizer controls one PLL's prospective schedule. All calls, including
// status reads, must be serialized with that PLL's owner. Do not copy it.
type Synchronizer struct {
	pll            *PLL
	cfg            SyncConfig
	state          SyncState
	epoch          uint64
	hasEpoch       bool
	anchorPending  bool
	referenceEpoch time.Duration
	lastReference  time.Duration
	lastObserved   time.Duration
	lastSlot       uint64
	hasSample      bool
	q              float64
	integral       float64
	lockCount      uint32
}

// NewSynchronizer creates a serialized reference controller for one PLL.
// Use only one active controller per PLL. Construction does not reserve the
// PLL exclusively; a corrected mapping is installed when trim first changes.
func NewSynchronizer(p *PLL, cfg SyncConfig) (*Synchronizer, error) {
	if p == nil || (cfg.ReferenceTimeAtVirtualZero == nil) == (!cfg.FirstSampleAnchoring) || !finitePositive(cfg.Kp) || !finitePositive(cfg.Ki) || !finitePositive(cfg.MaxCorrectionPPM) || cfg.MaxCorrectionPPM > 1_000_000 || !finitePositive(cfg.SlewLimitPPMPerSecond) || cfg.SlewLimitPPMPerSecond > 1_000_000 || cfg.MaxSampleAge <= 0 || cfg.MaxReferenceInterval <= 0 || cfg.MaxPhaseResidual <= 0 || cfg.LockTolerance < 0 || cfg.LockSamples == 0 {
		return nil, ErrInvalidSyncConfig
	}
	if p.syncClock != nil {
		return nil, ErrInvalidSyncConfig
	}
	// Config is a snapshot; caller mutation must not silently change the
	// controller's reference epoch after construction.
	if cfg.ReferenceTimeAtVirtualZero != nil {
		referenceEpoch := *cfg.ReferenceTimeAtVirtualZero
		cfg.ReferenceTimeAtVirtualZero = &referenceEpoch
	}
	s := &Synchronizer{pll: p, cfg: cfg, state: SyncAcquiring}
	return s, nil
}
func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func finitePositive(v float64) bool    { return finiteNonnegative(v) && v > 0 }
func periodRat(t timebase) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).Mul(new(big.Int).SetUint64(t.bitsPerUnit), big.NewInt(1_000_000_000)), new(big.Int).SetUint64(t.rate))
}
func (s *Synchronizer) State() SyncState { return s.state }
func (s *Synchronizer) Trim() float64    { return s.q }

// Status reports holdover when the last valid reference has exceeded MaxSampleAge.
// It never changes the clock mapping or controller state.
func (s *Synchronizer) Status(now time.Duration) SyncState {
	if now < 0 || !s.hasSample || now < s.lastObserved || now-s.lastObserved > s.cfg.MaxSampleAge {
		if s.hasSample {
			return SyncHoldover
		}
		return s.state
	}
	return s.state
}

// Holdover marks reference absence while preserving the last bounded trim.
func (s *Synchronizer) Holdover() {
	if s.hasSample {
		s.state = SyncHoldover
	}
}

func (s *Synchronizer) resetLock() {
	s.lockCount = 0
	if s.hasSample {
		s.state = SyncReacquiring
	}
}

// Observe updates a fixed-gain type-II controller. Positive phase error lengthens
// the virtual period. All state and PLL calls are single-owner/serialized.
func (s *Synchronizer) Observe(now time.Duration, sample ReferenceSample) error {
	if now < 0 || sample.ObservedAt < 0 || sample.ObservedAt > now || now-sample.ObservedAt > s.cfg.MaxSampleAge {
		s.resetLock()
		return ErrStaleReference
	}
	e := sample.Emission
	if e.owner != s.pll.owner || e.Slot != e.assignedSlot || e.UnitIndex != e.assignedUnit || e.Timestamp != e.assignedTimestamp {
		s.resetLock()
		return ErrInvalidEmission
	}
	if e.Slot >= s.pll.nextSlot {
		s.resetLock()
		return ErrUnassignedEmission
	}
	if s.hasEpoch && sample.Epoch != s.epoch {
		s.resetLock()
		return ErrEpochChange
	}
	if s.hasSample && (e.Slot <= s.lastSlot || sample.ReferenceTime <= s.lastReference || sample.ObservedAt <= s.lastObserved) {
		s.resetLock()
		return ErrReferenceOrder
	}
	referenceEpoch := s.referenceEpoch
	if !s.hasEpoch || s.anchorPending {
		if s.cfg.ReferenceTimeAtVirtualZero != nil {
			referenceEpoch = *s.cfg.ReferenceTimeAtVirtualZero
		} else {
			epoch := new(big.Int).Sub(big.NewInt(int64(sample.ReferenceTime)), big.NewInt(int64(e.assignedTimestamp)))
			if !epoch.IsInt64() {
				s.resetLock()
				return ErrInvalidReference
			}
			referenceEpoch = time.Duration(epoch.Int64())
		}
	}
	phaseBig := new(big.Int).Sub(big.NewInt(int64(sample.ReferenceTime)), big.NewInt(int64(referenceEpoch)))
	phaseBig.Sub(phaseBig, big.NewInt(int64(e.Timestamp)))
	if !phaseBig.IsInt64() {
		s.resetLock()
		return ErrPhaseResidual
	}
	phase := time.Duration(phaseBig.Int64())
	if phase > s.cfg.MaxPhaseResidual || phase < -s.cfg.MaxPhaseResidual {
		s.resetLock()
		return ErrPhaseResidual
	}
	if s.hasSample && new(big.Int).Sub(big.NewInt(int64(sample.ReferenceTime)), big.NewInt(int64(s.lastReference))).Cmp(big.NewInt(int64(s.cfg.MaxReferenceInterval))) > 0 {
		// The independently validated phase is continuous; only interval-based
		// controller history is discarded during reacquisition.
		s.lastReference = sample.ReferenceTime
		s.lastObserved = sample.ObservedAt
		s.lastSlot = e.Slot
		s.integral = 0
		s.lockCount = 0
		s.state = SyncReacquiring
		return nil
	}
	oldQ := s.q
	if s.hasSample {
		delta := new(big.Int).Sub(big.NewInt(int64(sample.ReferenceTime)), big.NewInt(int64(s.lastReference)))
		deltaFloat, _ := delta.Float64()
		dt := deltaFloat / float64(time.Second)
		eSec := float64(phase) / float64(time.Second)
		limit := s.cfg.MaxCorrectionPPM / 1e6
		lowerLimit := math.Max(-limit, s.minimumTrim())
		integralStep := s.cfg.Ki * eSec * dt
		candidateI := s.integral + integralStep
		if math.IsInf(candidateI, 1) {
			candidateI = limit
		} else if math.IsInf(candidateI, -1) {
			candidateI = -limit
		}
		candidateI = math.Max(-limit, math.Min(limit, candidateI))
		pTerm := s.cfg.Kp * eSec
		if math.IsInf(pTerm, 1) {
			pTerm = 2 * limit
		} else if math.IsInf(pTerm, -1) {
			pTerm = -2 * limit
		}
		rawTarget := pTerm + candidateI
		target := math.Max(lowerLimit, math.Min(limit, rawTarget))
		hardLimited := rawTarget > limit && eSec > 0 || rawTarget < lowerLimit && eSec < 0
		maxStep := s.cfg.SlewLimitPPMPerSecond / 1e6 * dt
		slewLimited := target > s.q+maxStep && eSec > 0 || target < s.q-maxStep && eSec < 0
		if hardLimited || slewLimited {
			// Conditional integration: do not accumulate error that pushes
			// farther into either the correction or slew limit.
			candidateI = s.integral
			rawTarget = pTerm + candidateI
			target = math.Max(lowerLimit, math.Min(limit, rawTarget))
		}
		if target > s.q+maxStep {
			target = s.q + maxStep
		}
		if target < s.q-maxStep {
			target = s.q - maxStep
		}
		s.q, s.integral = s.quantizeTrim(target, delta), candidateI
	}
	if s.q != oldQ {
		s.reanchor()
	}
	if !s.hasEpoch {
		s.epoch = sample.Epoch
		s.hasEpoch = true
	}
	s.anchorPending = false
	s.referenceEpoch = referenceEpoch
	s.lastReference = sample.ReferenceTime
	s.lastObserved = sample.ObservedAt
	s.lastSlot = e.Slot
	s.hasSample = true
	if phase >= -s.cfg.LockTolerance && phase <= s.cfg.LockTolerance {
		if s.lockCount < s.cfg.LockSamples {
			s.lockCount++
		}
	} else {
		s.lockCount = 0
	}
	if s.lockCount >= s.cfg.LockSamples {
		s.state = SyncLocked
	} else if s.state != SyncReacquiring {
		s.state = SyncAcquiring
	}
	return nil
}
func (s *Synchronizer) minimumTrim() float64 {
	minimum := new(big.Rat).Quo(big.NewRat(1, 1), periodRat(s.pll.clock))
	minQ := new(big.Rat).Sub(minimum, big.NewRat(1, 1))
	minScaled := new(big.Int).Mul(minQ.Num(), big.NewInt(correctionDenominator))
	minScaled.Quo(minScaled, minQ.Denom())
	return float64(minScaled.Int64()) / float64(correctionDenominator)
}

// Quantize inward in integer trim units, so rounding cannot enlarge a hard
// bound or a per-reference slew step. Sub-grid steps leave the trim unchanged.
func (s *Synchronizer) quantizeTrim(target float64, delta *big.Int) float64 {
	hard := new(big.Rat).SetFloat64(s.cfg.MaxCorrectionPPM)
	hard.Mul(hard, big.NewRat(correctionDenominator, 1_000_000))
	hardUnits := new(big.Int).Quo(hard.Num(), hard.Denom()).Int64()
	slew := new(big.Rat).SetFloat64(s.cfg.SlewLimitPPMPerSecond)
	slew.Mul(slew, new(big.Rat).SetInt(delta))
	slew.Mul(slew, big.NewRat(correctionDenominator, 1_000_000_000_000_000))
	step := new(big.Int).Quo(slew.Num(), slew.Denom())
	// A larger step cannot constrain the supported [-1,1] trim interval.
	stepUnits := 2 * correctionDenominator
	if step.Cmp(big.NewInt(stepUnits)) < 0 {
		stepUnits = step.Int64()
	}
	previous := int64(math.Round(s.q * float64(correctionDenominator)))
	lower := max(-hardUnits, previous-stepUnits, int64(math.Round(s.minimumTrim()*float64(correctionDenominator))))
	upper := min(hardUnits, previous+stepUnits)
	units := int64(math.Round(target * float64(correctionDenominator)))
	units = max(lower, min(upper, units))
	return float64(units) / float64(correctionDenominator)
}

func (s *Synchronizer) reanchor() {
	p := s.pll
	old := p.syncClock
	anchor := new(big.Rat)
	if old != nil {
		anchor = old.exact(p.nextSlot)
	} else {
		anchor = new(big.Rat).SetFrac(new(big.Int).Mul(new(big.Int).SetUint64(p.nextSlot), new(big.Int).Mul(new(big.Int).SetUint64(p.clock.bitsPerUnit), big.NewInt(1_000_000_000))), new(big.Int).SetUint64(p.clock.rate))
	}
	// The segment is anchored at the first slot not yet assigned, never at the reference slot.
	q := int64(math.Round(s.q * float64(correctionDenominator)))
	if q == 0 && old == nil {
		s.q = 0
		return
	}
	// quantizeTrim already enforces hard, slew and minimum-period bounds.
	s.q = float64(q) / float64(correctionDenominator)
	factor := new(big.Rat).SetFrac(big.NewInt(correctionDenominator+q), big.NewInt(correctionDenominator))
	period := new(big.Rat).Mul(periodRat(p.clock), factor)
	p.syncClock = &correctedClock{start: p.nextSlot, anchor: anchor, period: period, q: q}
}

// ResetEpoch explicitly permits reacquisition after a source epoch change.
// Known-epoch mode requires the new reference time at virtual zero; relative
// mode takes no mapping argument and anchors on its next accepted sample.
func (s *Synchronizer) ResetEpoch(epoch uint64, referenceTimeAtVirtualZero ...time.Duration) error {
	if s.cfg.FirstSampleAnchoring {
		if len(referenceTimeAtVirtualZero) != 0 {
			return ErrInvalidSyncConfig
		}
	} else {
		if len(referenceTimeAtVirtualZero) != 1 {
			return ErrInvalidSyncConfig
		}
		value := referenceTimeAtVirtualZero[0]
		s.cfg.ReferenceTimeAtVirtualZero = &value
	}
	s.epoch = epoch
	s.hasEpoch = true
	s.hasSample = false
	s.anchorPending = true
	s.integral = 0
	s.lockCount = 0
	s.state = SyncReacquiring
	return nil
}
