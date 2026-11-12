// doc.go is complete; do not change it.

// Package pll keeps time for programs that must produce or consume things at
// a steady rate, or follow a clock other than their own.
//
// A Pacer divides time into evenly spaced, numbered slots at an exact Rate and
// says which slots are due. Every slot's time is computed from its number with
// exact rational arithmetic, so it never drifts, even at rates such as
// 30000/1001 units per second. A Pacer can run slightly faster or slower than
// its nominal rate through a trim, given in parts per trillion.
//
// A Follower is a phase-locked loop. Fed readings of a remote clock taken
// against the local one, it learns how far apart the two clocks are and how
// fast the remote one runs, filters out delivery jitter and outliers, notices
// when the remote clock jumps, and maps any remote time to local time and
// back. Its Trim can drive a Pacer, so output keeps pace with a device that
// runs on its own clock.
//
// The package never reads a clock, starts a goroutine or touches your data.
// Every call that depends on the present takes a time you pass in, a duration
// from an origin you choose, and the hot paths allocate nothing.
package pll
