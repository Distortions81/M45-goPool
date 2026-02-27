package main

import "math"

// 16-bit SI quantization for wide-range positive values (e.g. hashrate).
//
// Layout:
//   high 4 bits: SI exponent bucket (base 1000)
//   low 12 bits: quantized mantissa in [1, 1000)
//
// code 0 is reserved for value 0.
const (
	siQuant16ExpBits  = 3
	siQuant16MantBits = 16 - siQuant16ExpBits

	siQuant16MantMask = (1 << siQuant16MantBits) - 1
	siQuant16MaxExp   = (1 << siQuant16ExpBits) - 1
	siQuant16MantMin  = 1.0
	siQuant16MantMax  = 1000.0
	siQuant16Base     = 1000.0
	siQuant16QLevels  = siQuant16MantMask

	siQuant8ExpBits  = 3
	siQuant8MantBits = 8 - siQuant8ExpBits

	siQuant8MantMask = (1 << siQuant8MantBits) - 1
	siQuant8MaxExp   = (1 << siQuant8ExpBits) - 1
	siQuant8Base     = 1000.0
	siQuant8QLevels  = siQuant8MantMask
)

// encodeSIQuant16 packs a non-negative value into a 16-bit exponent+mantissa
// code. It is monotonic for non-negative finite inputs (after saturation).
func encodeSIQuant16(v float64) uint16 {
	if !(v > 0) || math.IsNaN(v) {
		return 0
	}
	if math.IsInf(v, 1) {
		return uint16((siQuant16MaxExp << siQuant16MantBits) | siQuant16MantMask)
	}

	exp := uint16(0)
	mant := v
	for mant >= siQuant16MantMax && exp < siQuant16MaxExp {
		mant /= siQuant16Base
		exp++
	}

	// If we saturated exponent, clamp mantissa to the representable range.
	if exp == siQuant16MaxExp && mant >= siQuant16MantMax {
		return uint16((exp << siQuant16MantBits) | siQuant16MantMask)
	}
	if mant < siQuant16MantMin {
		mant = siQuant16MantMin
	}
	if mant >= siQuant16MantMax {
		// Guard against roundoff at bucket boundaries.
		mant = math.Nextafter(siQuant16MantMax, 0)
	}

	// Quantize mantissa logarithmically across [1,1000). This keeps relative
	// error roughly uniform across the SI bucket.
	logT := math.Log(mant) / math.Log(siQuant16Base) // [0,1)
	if logT < 0 {
		logT = 0
	}
	if logT > 1 {
		logT = 1
	}
	q := 1 + int(math.Round(logT*float64(siQuant16QLevels-1)))
	if q < 1 {
		q = 1
	}
	if q > siQuant16MantMask {
		q = siQuant16MantMask
	}

	return uint16((exp << siQuant16MantBits) | uint16(q))
}

// decodeSIQuant16 reconstructs the approximate value from encodeSIQuant16.
func decodeSIQuant16(code uint16) float64 {
	if code == 0 {
		return 0
	}
	exp := int((code >> siQuant16MantBits) & uint16(siQuant16MaxExp))
	q := int(code & siQuant16MantMask)
	if q <= 0 {
		return 0
	}

	logT := float64(q-1) / float64(siQuant16QLevels-1)
	mant := math.Pow(siQuant16Base, logT)

	v := mant
	for i := 0; i < exp; i++ {
		v *= siQuant16Base
	}
	return v
}

// encodeSIQuant8 packs a non-negative value into an 8-bit exponent+mantissa
// code. code 0 is reserved for value 0.
func encodeSIQuant8(v float64) uint8 {
	if !(v > 0) || math.IsNaN(v) {
		return 0
	}
	if math.IsInf(v, 1) {
		return uint8((siQuant8MaxExp << siQuant8MantBits) | siQuant8MantMask)
	}

	exp := uint8(0)
	mant := v
	for mant >= siQuant16MantMax && exp < siQuant8MaxExp {
		mant /= siQuant8Base
		exp++
	}
	if exp == siQuant8MaxExp && mant >= siQuant16MantMax {
		return uint8((exp << siQuant8MantBits) | siQuant8MantMask)
	}
	if mant < siQuant16MantMin {
		mant = siQuant16MantMin
	}
	if mant >= siQuant16MantMax {
		mant = math.Nextafter(siQuant16MantMax, 0)
	}

	logT := math.Log(mant) / math.Log(siQuant8Base)
	if logT < 0 {
		logT = 0
	}
	if logT > 1 {
		logT = 1
	}
	q := 1 + int(math.Round(logT*float64(siQuant8QLevels-1)))
	if q < 1 {
		q = 1
	}
	if q > siQuant8MantMask {
		q = siQuant8MantMask
	}

	return uint8((exp << siQuant8MantBits) | uint8(q))
}

// decodeSIQuant8 reconstructs the approximate value from encodeSIQuant8.
func decodeSIQuant8(code uint8) float64 {
	if code == 0 {
		return 0
	}
	exp := int((code >> siQuant8MantBits) & uint8(siQuant8MaxExp))
	q := int(code & siQuant8MantMask)
	if q <= 0 {
		return 0
	}

	logT := float64(q-1) / float64(siQuant8QLevels-1)
	mant := math.Pow(siQuant8Base, logT)

	v := mant
	for i := 0; i < exp; i++ {
		v *= siQuant8Base
	}
	return v
}

// encodeHashrateSI16 is a semantic alias for hashrate storage.
func encodeHashrateSI16(hashrate float64) uint16 { return encodeSIQuant16(hashrate) }

// decodeHashrateSI16 decodes a hashrate value encoded by encodeHashrateSI16.
func decodeHashrateSI16(code uint16) float64 { return decodeSIQuant16(code) }

// encodeHashrateSI8 is a semantic alias for compact hashrate chart storage.
func encodeHashrateSI8(hashrate float64) uint8 { return encodeSIQuant8(hashrate) }

// decodeHashrateSI8 decodes a hashrate value encoded by encodeHashrateSI8.
func decodeHashrateSI8(code uint8) float64 { return decodeSIQuant8(code) }

// encodeBestShareSI16 is a semantic alias for best-share difficulty storage.
func encodeBestShareSI16(diff float64) uint16 { return encodeSIQuant16(diff) }

// decodeBestShareSI16 decodes a best-share difficulty encoded by encodeBestShareSI16.
func decodeBestShareSI16(code uint16) float64 { return decodeSIQuant16(code) }
