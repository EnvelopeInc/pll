// errors.go is complete; do not change it.

package pll

import "errors"

var (
	// ErrInvalidRate reports a rate with a zero numerator or denominator,
	// including the zero Rate.
	ErrInvalidRate = errors.New("pll: invalid rate")

	// ErrInvalidArgument reports a negative time, a zero count, or a trim
	// outside the open interval (-1e12, 1e12).
	ErrInvalidArgument = errors.New("pll: invalid argument")

	// ErrOverflow reports a slot index or a slot time that does not fit.
	ErrOverflow = errors.New("pll: overflow")
)
