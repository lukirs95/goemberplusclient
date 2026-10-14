package ber

import (
	"fmt"
	"math"
	"math/bits"
)

// Special REAL values (X.690 8.5.9).
const (
	realPlusInfinity  = 0x40
	realMinusInfinity = 0x41
	realNaN           = 0x42
	realMinusZero     = 0x43
)

// appendReal appends the contents of a REAL in binary form, base 2, with an
// odd mantissa (spec: "Real [X.690 8.5]").
func appendReal(b []byte, v float64) []byte {
	switch {
	case v == 0 && !math.Signbit(v):
		return b
	case v == 0:
		return append(b, realMinusZero)
	case math.IsInf(v, 1):
		return append(b, realPlusInfinity)
	case math.IsInf(v, -1):
		return append(b, realMinusInfinity)
	case math.IsNaN(v):
		return append(b, realNaN)
	}

	first := byte(0x80)
	if v < 0 {
		first |= 0x40
		v = -v
	}
	frac, exp := math.Frexp(v)
	mantissa := uint64(math.Ldexp(frac, 53))
	exp -= 53
	shift := bits.TrailingZeros64(mantissa)
	mantissa >>= shift
	exp += shift

	expSize := intSize(int64(exp))
	b = append(b, first|byte(expSize-1))
	b = appendIntBytes(b, int64(exp), expSize)
	for i := (bits.Len64(mantissa) - 1) / 8; i >= 0; i-- {
		b = append(b, byte(mantissa>>(8*i)))
	}
	return b
}

// parseReal decodes the contents of a REAL. Binary encodings with base 2, 8
// and 16 are supported; decimal encodings return ErrUnsupported.
func parseReal(data []byte) (float64, error) {
	if len(data) == 0 {
		return 0, nil
	}
	first := data[0]
	if first&0x80 == 0 {
		if first&0x40 == 0 {
			return 0, fmt.Errorf("%w: decimal REAL", ErrUnsupported)
		}
		if len(data) != 1 {
			return 0, fmt.Errorf("%w: special REAL of length %d", ErrInvalid, len(data))
		}
		switch first {
		case realPlusInfinity:
			return math.Inf(1), nil
		case realMinusInfinity:
			return math.Inf(-1), nil
		case realNaN:
			return math.NaN(), nil
		case realMinusZero:
			return math.Copysign(0, -1), nil
		}
		return 0, fmt.Errorf("%w: special REAL %#02x", ErrInvalid, first)
	}

	var baseBits int
	switch first >> 4 & 0x03 {
	case 0:
		baseBits = 1
	case 1:
		baseBits = 3
	case 2:
		baseBits = 4
	default:
		return 0, fmt.Errorf("%w: reserved REAL base", ErrInvalid)
	}
	scale := int(first >> 2 & 0x03)

	rest := data[1:]
	expSize := int(first&0x03) + 1
	if expSize == 4 {
		if len(rest) == 0 {
			return 0, fmt.Errorf("%w: REAL exponent length", ErrInvalid)
		}
		expSize = int(rest[0])
		rest = rest[1:]
	}
	if expSize == 0 || expSize > len(rest) {
		return 0, fmt.Errorf("%w: REAL exponent", ErrInvalid)
	}
	exp, ok := parseInt(rest[:expSize])
	if !ok {
		return 0, fmt.Errorf("%w: REAL exponent of %d bytes", ErrUnsupported, expSize)
	}
	rest = rest[expSize:]
	if len(rest) > 8 {
		return 0, fmt.Errorf("%w: REAL mantissa of %d bytes", ErrUnsupported, len(rest))
	}
	var mantissa uint64
	for _, c := range rest {
		mantissa = mantissa<<8 | uint64(c)
	}

	// Results outside ±2^limit are infinite or zero anyway; clamping keeps
	// the multiplication below from overflowing.
	const limit = 1 << 16
	exp = max(min(exp, limit), -limit)
	v := math.Ldexp(float64(mantissa), scale+int(exp)*baseBits)
	if first&0x40 != 0 {
		v = -v
	}
	return v, nil
}
