// doc.go is complete; do not change it.

// Package pll keeps time for programs that must produce or consume things at
// a steady rate.
//
// A Pacer divides time into evenly spaced, numbered slots at an exact Rate and
// says which slots are due. Every slot's time is computed from its number with
// exact rational arithmetic, so it never drifts, even at rates such as
// 30000/1001 units per second. A Pacer can run slightly faster or slower than
// its nominal rate through a trim, given in parts per trillion.
//
// The package never reads a clock, starts a goroutine or touches your data.
// Every call that depends on the present takes now, a duration from an origin
// you choose, and the hot paths allocate nothing.
package pll
