package main

import (
	"math/bits"
	"testing"
)

// TestUpdateVersionMaskTransitions exercises the key transitions of
// MinerConn.updateVersionMask:
// - no miner mask (pool mask applied directly)
// - enabling version-rolling when a miner mask is present
// - shrinking the pool mask so the intersection goes to zero
// - clamping minVerBits when the intersection loses bits.
func TestUpdateVersionMaskTransitions(t *testing.T) {
	const standardMask = uint32(0x1fffe000)

	t.Run("noMinerMaskUsesPoolMask", func(t *testing.T) {
		mc := &MinerConn{}

		changed := mc.updateVersionMask(standardMask)

		if !changed {
			t.Fatalf("expected change when setting initial pool mask")
		}
		if mc.poolMask != standardMask {
			t.Fatalf("poolMask mismatch: got %#08x want %#08x", mc.poolMask, standardMask)
		}
		if mc.versionMask != standardMask {
			t.Fatalf("versionMask mismatch: got %#08x want %#08x", mc.versionMask, standardMask)
		}
		if mc.versionRoll {
			t.Fatalf("versionRoll should remain false when no miner mask is negotiated")
		}
	})

	t.Run("enableVersionRollWithMinerMask", func(t *testing.T) {
		mc := &MinerConn{
			minVerBits: 3,
		}
		// Simulate a miner mask negotiated via mining.configure.
		minerMask := uint32(0x00ff0000)
		mc.minerMask = minerMask

		changed := mc.updateVersionMask(standardMask)

		if !changed {
			t.Fatalf("expected change when enabling version rolling with miner mask")
		}
		final := standardMask & minerMask
		if final == 0 {
			t.Fatalf("test setup error: standardMask & minerMask must be non-zero")
		}
		if mc.versionMask != final {
			t.Fatalf("versionMask mismatch: got %#08x want %#08x", mc.versionMask, final)
		}
		if !mc.versionRoll {
			t.Fatalf("expected versionRoll=true after applying miner mask")
		}
		available := bits.OnesCount32(final)
		if mc.minVerBits <= 0 || mc.minVerBits > available {
			t.Fatalf("minVerBits out of range after update: got %d, available %d", mc.minVerBits, available)
		}
	})

	t.Run("shrinkPoolMaskToZeroIntersection", func(t *testing.T) {
		// Start from a state where version rolling is enabled with a non-zero intersection.
		mc := &MinerConn{}
		mc.minerMask = standardMask
		_ = mc.updateVersionMask(standardMask)
		if !mc.versionRoll || mc.versionMask == 0 {
			t.Fatalf("expected versionRoll enabled with non-zero mask")
		}

		// Now apply a pool mask that has no bits in common with minerMask.
		newPoolMask := uint32(0x00000001)
		if standardMask&newPoolMask != 0 {
			t.Fatalf("test setup error: expected zero intersection between masks")
		}
		changed := mc.updateVersionMask(newPoolMask)

		if !changed {
			t.Fatalf("expected change when intersection shrinks to zero")
		}
		if mc.versionMask != 0 {
			t.Fatalf("versionMask should be zero when intersection is empty, got %#08x", mc.versionMask)
		}
		if mc.versionRoll {
			t.Fatalf("versionRoll should be disabled when intersection is empty")
		}
	})

	t.Run("clampMinVerBitsWhenIntersectionShrinks", func(t *testing.T) {
		// Start with a wider intersection supporting many bits.
		mc := &MinerConn{}
		widePool := uint32(0x00ff0000)
		mc.minerMask = widePool
		mc.minVerBits = 8
		_ = mc.updateVersionMask(widePool)
		if mc.versionMask == 0 || !mc.versionRoll {
			t.Fatalf("expected version rolling enabled with wide mask")
		}

		// Now shrink pool mask so only a couple of bits remain.
		narrowPool := uint32(0x00030000) // at most 2 bits set
		changed := mc.updateVersionMask(narrowPool)

		if !changed {
			t.Fatalf("expected change when shrinking pool mask")
		}
		final := narrowPool & mc.minerMask
		available := bits.OnesCount32(final)
		if available == 0 {
			t.Fatalf("test setup error: expected non-zero intersection for narrow mask")
		}
		if mc.versionMask != final {
			t.Fatalf("versionMask mismatch after shrink: got %#08x want %#08x", mc.versionMask, final)
		}
		if mc.minVerBits > available {
			t.Fatalf("minVerBits should be clamped to available bits: got %d, available %d", mc.minVerBits, available)
		}
	})
}
