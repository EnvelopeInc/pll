// follower_errors.go is complete; do not change it.

package pll

import "errors"

var (
	// ErrOrder reports a reading whose local time went backwards or whose
	// remote ticks did not increase. The reading is ignored.
	ErrOrder = errors.New("pll: reading out of order")

	// ErrNotAnchored reports a mapping requested before the first reading.
	ErrNotAnchored = errors.New("pll: follower has no reading yet")
)

// ConfigError reports an invalid FollowerConfig field.
type ConfigError struct {
	Field  string // the field name, such as "Bandwidth"
	Reason string // what the field must satisfy, such as "must be finite and > 0"
}

// Error returns, for example, "pll: FollowerConfig.Bandwidth must be finite and > 0".
func (e *ConfigError) Error() string { return "pll: FollowerConfig." + e.Field + " " + e.Reason }
