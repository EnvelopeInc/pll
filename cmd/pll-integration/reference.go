package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/EnvelopeInc/pll"
)

const referencePeriod = 10 * time.Millisecond
const associationMaxAge = 20 * time.Millisecond

// A whole immutable snapshot preserves the library's private provenance. The
// single-slot mailbox deliberately replaces old snapshots rather than blocking
// the scheduler or building an unbounded association backlog.
type emissionSnapshot struct {
	emission pll.Emission
	at       time.Duration
}

func publishSnapshot(mailbox chan emissionSnapshot, snapshot emissionSnapshot) {
	select {
	case mailbox <- snapshot:
		return
	default:
	}
	select {
	case <-mailbox:
	default:
	}
	select {
	case mailbox <- snapshot:
	default:
	}
}

type referenceTick struct{ at, value time.Duration }

func makeReferenceTick(at time.Duration) referenceTick {
	jitter := (int64(at/referencePeriod)%7 - 3) * int64(time.Microsecond)
	return referenceTick{at: at, value: scaleReference(at) + time.Duration(jitter)}
}
func scaleReference(at time.Duration) time.Duration {
	// The bounded exercise lasts at most 12s, so this product fits int64.
	return time.Duration(int64(at) * 1_000_000 / 1_000_050)
}
func referenceInterrupted(at time.Duration) bool { return at >= 4*time.Second && at < 6*time.Second }
func associateReference(tick referenceTick, snapshot emissionSnapshot) (pll.ReferenceSample, bool) {
	if snapshot.at > tick.at || tick.at-snapshot.at > associationMaxAge {
		return pll.ReferenceSample{}, false
	}
	// This is an ideal slot-coordinate association, NOT a hardware timestamp:
	// project the independently sampled clock to the nominal coordinate of the
	// genuine emitted slot. Never use the PLL's corrected timestamp as a clock.
	nominal := sourceDeadline(snapshot.emission.Slot, 0)
	return pll.ReferenceSample{Emission: snapshot.emission, ReferenceTime: tick.value + scaleReference(nominal-tick.at), ObservedAt: tick.at}, true
}

type referenceSourceStats struct {
	ticks, interrupted, unassociated, submitted uint64
	phase                                       []int64
	rejected                                    map[string]uint64
}

// runReference owns no PLL or Synchronizer. Tick generation proceeds even when
// no emissions exist; only sample association depends on the bounded mailbox.
func runReference(ctx context.Context, origin time.Time, mailbox <-chan emissionSnapshot, refs *pll.ReferenceQueue) referenceSourceStats {
	ticker := time.NewTicker(referencePeriod)
	defer ticker.Stop()
	return collectReferences(ctx, origin, ticker.C, mailbox, refs)
}
func collectReferences(ctx context.Context, origin time.Time, ticks <-chan time.Time, mailbox <-chan emissionSnapshot, refs *pll.ReferenceQueue) referenceSourceStats {
	stats := referenceSourceStats{rejected: map[string]uint64{}}
	var latest emissionSnapshot
	have, used := false, false
	var lastSlot uint64
	for {
		select {
		case <-ctx.Done():
			return stats
		case at, ok := <-ticks:
			if !ok {
				return stats
			}
			tick := makeReferenceTick(at.Sub(origin))
			stats.ticks++
			if referenceInterrupted(tick.at) {
				stats.interrupted++
				continue
			}
			select {
			case latest = <-mailbox:
				have = true
			default:
			}
			if !have || (used && latest.emission.Slot <= lastSlot) {
				stats.unassociated++
				continue
			}
			sample, ok := associateReference(tick, latest)
			if !ok {
				stats.unassociated++
				continue
			}
			if err := refs.Submit(ctx, sample); err != nil {
				stats.rejected[err.Error()]++
			} else {
				stats.submitted++
				stats.phase = append(stats.phase, int64(sample.ReferenceTime-sample.Emission.Timestamp))
				lastSlot, used = latest.emission.Slot, true
			}
		}
	}
}

type stateTransition struct {
	at    time.Duration
	state pll.SyncState
}
type referenceTelemetry struct {
	transitions                 []stateTransition
	durations                   [4]time.Duration
	lastAt                      time.Duration
	lastState                   pll.SyncState
	finalTrim, minTrim, maxTrim float64
	steadyStart                 time.Duration
	steadyIntegral              float64
	steadyDuration              time.Duration
}

func newReferenceTelemetry(duration time.Duration) referenceTelemetry {
	return referenceTelemetry{minTrim: math.Inf(1), maxTrim: math.Inf(-1), steadyStart: duration - time.Second}
}
func (m *referenceTelemetry) observe(at time.Duration, state pll.SyncState, trim float64) {
	if len(m.transitions) == 0 {
		m.transitions = append(m.transitions, stateTransition{0, state})
	} else {
		m.durations[m.lastState] += at - m.lastAt
		start := m.lastAt
		if start < m.steadyStart {
			start = m.steadyStart
		}
		if at > start {
			m.steadyIntegral += m.finalTrim * (at - start).Seconds()
			m.steadyDuration += at - start
		}
		if state != m.lastState {
			m.transitions = append(m.transitions, stateTransition{at, state})
		}
	}
	m.lastAt, m.lastState, m.finalTrim = at, state, trim
	m.minTrim = math.Min(m.minTrim, trim)
	m.maxTrim = math.Max(m.maxTrim, trim)
}
func (m *referenceTelemetry) summary() string {
	transitions := make([]string, len(m.transitions))
	for i, v := range m.transitions {
		transitions[i] = fmt.Sprintf("%s@%s", stateName(v.state), v.at)
	}
	steady := "unavailable"
	if m.steadyDuration > 0 {
		steady = fmt.Sprintf("%.3f", m.steadyIntegral/m.steadyDuration.Seconds())
	}
	return fmt.Sprintf("observed_transitions=[%s] observed_state_durations={acquiring:%s,locked:%s,holdover:%s,reacquiring:%s} trim_final_ppm=%.3f trim_last_1s_time_weighted_mean_ppm=%s trim_steady_coverage=%s", strings.Join(transitions, ","), m.durations[0], m.durations[1], m.durations[2], m.durations[3], m.finalTrim, steady, m.steadyDuration)
}
