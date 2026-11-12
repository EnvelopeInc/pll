// status.go is complete; do not change it.

package pll

import "strconv"

// Status says what Observe did with a reading.
type Status uint8

const (
	// StatusAnchored: the first reading after NewFollower or Reset set the mapping.
	StatusAnchored Status = iota + 1
	// StatusAccepted: a sample was fed to the loop and used.
	StatusAccepted
	// StatusPending: the reading joined the current lower-envelope window.
	StatusPending
	// StatusRejected: the sample fed to the loop was an outlier and was ignored.
	StatusRejected
	// StatusStepped: enough agreeing outliers showed that the remote clock
	// jumped; the loop re-anchored on the latest one.
	StatusStepped
)

var statusNames = [...]string{"", "anchored", "accepted", "pending", "rejected", "stepped"}

// String returns the status in lower case, such as "accepted", or "Status(9)".
func (s Status) String() string {
	if s >= StatusAnchored && s <= StatusStepped {
		return statusNames[s]
	}
	return "Status(" + strconv.Itoa(int(s)) + ")"
}

// State is a follower's lock state.
type State uint8

const (
	// StateAcquiring: anchored or not yet, but not locked.
	StateAcquiring State = iota
	// StateLocked: LockCount consecutive accepted samples were within LockThreshold.
	StateLocked
	// StateHoldover: no sample accepted for more than HoldoverAfter; the
	// mapping keeps running at the last rate estimate.
	StateHoldover
)

var stateNames = [...]string{"acquiring", "locked", "holdover"}

// String returns the state in lower case, such as "locked", or "State(9)".
func (s State) String() string {
	if s <= StateHoldover {
		return stateNames[s]
	}
	return "State(" + strconv.Itoa(int(s)) + ")"
}
