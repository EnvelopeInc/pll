package pll_test

// A literal model of the follower's loop: the equations from SPEC.md, part 2,
// with no window, no outlier rejection, no lock or jitter, and the clamp only
// when asked for. The follower tests compare against it where none of the
// missing parts apply.

import (
	"math"
	"time"

	"github.com/version2beta/pll"
)

type loopModel struct {
	pn, wn, zeta float64
	max          float64 // 0 means no clamp
	anchored     bool
	l0           float64
	r0           int64
	y            float64
	last         time.Duration
}

func newLoopModel(cfg pll.FollowerConfig, clamp bool) *loopModel {
	m := &loopModel{
		pn:   float64(cfg.Nominal.Den()) * 1e9 / float64(cfg.Nominal.Num()),
		wn:   2 * math.Pi * cfg.Bandwidth,
		zeta: cfg.Damping,
	}
	if clamp {
		m.max = cfg.MaxOffsetPPM * 1e-6
	}
	return m
}

func (m *loopModel) predict(r int64) float64 {
	return m.l0 + float64(r-m.r0)*m.pn/(1+m.y)
}

// observe applies one reading and returns its phase error in ns.
func (m *loopModel) observe(l time.Duration, r int64) float64 {
	if !m.anchored {
		m.anchored, m.l0, m.r0, m.last = true, float64(l), r, l
		return 0
	}
	p := m.predict(r)
	e := float64(l) - p
	dt := (l - m.last).Seconds()
	if dt > 0 {
		x := m.wn * math.Min(dt, 0.5/m.wn)
		kp := math.Min(1, 2*m.zeta*x)
		m.y -= x * x / dt * e / 1e9
		if m.max > 0 {
			m.y = math.Max(-m.max, math.Min(m.max, m.y))
		}
		m.l0, m.r0, m.last = p+kp*e, r, l
	}
	return e
}

func (m *loopModel) local(r int64) time.Duration { return time.Duration(math.Round(m.predict(r))) }
