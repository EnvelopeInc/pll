package pll

// Rate is an exact number of units per second, held as a fraction reduced
// to lowest terms. Construct one with UnitsPerSecond or BitsPerSecond; the
// zero Rate is invalid.
type Rate struct {
	num, den uint64
}

// UnitsPerSecond returns the rate num/den units per second, reduced to lowest
// terms: UnitsPerSecond(60000, 2002) has Num 30000 and Den 1001. A zero num
// or den returns the zero Rate and ErrInvalidRate.
func UnitsPerSecond(num, den uint64) (Rate, error) {
	return Rate{}, nil
}

// BitsPerSecond returns the rate of bitsPerUnit-bit units that carries bits
// bits per second: bits/bitsPerUnit units per second, reduced to lowest
// terms. BitsPerSecond(50_000_000, 8) is 6250000 units (bytes) per second. A
// zero argument returns the zero Rate and ErrInvalidRate.
func BitsPerSecond(bits, bitsPerUnit uint64) (Rate, error) {
	return Rate{}, nil
}

// Num returns the numerator of the reduced fraction.
func (r Rate) Num() uint64 { return 0 }

// Den returns the denominator of the reduced fraction.
func (r Rate) Den() uint64 { return 0 }

// String returns "N units/s" when the denominator is 1, "N/D units/s"
// otherwise, and "invalid rate" for the zero Rate.
func (r Rate) String() string { return "" }
