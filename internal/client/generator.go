package client

import (
	"math/rand/v2"

	"pq-heal/internal/protocol"
)

type ValueGenerator struct {
	dataType protocol.DataType
	rng      *rand.Rand
	last     any
	setLast  bool
}

// NewValueGenerator seeds a per-client RNG so values are varied but
// independent across generators. math/rand/v2 is auto-seeded and goroutine-safe
// via the default Source; we create a dedicated Rand so each generator's
// sequence is independent of global state.
func NewValueGenerator(dataType protocol.DataType) *ValueGenerator {
	return &ValueGenerator{
		dataType: dataType,
		rng:      rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())),
	}
}

// Next returns a new sample, nudging it if it matches the previous value.
func (g *ValueGenerator) Next() any {
	next := g.generate()
	if g.setLast && valuesEqual(next, g.last) {
		next = g.forceDifferent(next)
	}
	g.last = next
	g.setLast = true
	return next
}

// generate creates a raw random value for the configured data type.
func (g *ValueGenerator) generate() any {
	switch g.dataType {
	case protocol.DataHeartRate:
		return g.randomFloat(60, 100)
	case protocol.DataPersonTemp:
		return g.randomFloat(36.0, 38.0)
	case protocol.DataAmbientTemp:
		return g.randomFloat(18.0, 30.0)
	case protocol.DataMotion:
		return g.rng.IntN(2) == 0
	default:
		return 0.0
	}
}

func (g *ValueGenerator) randomFloat(min, max float64) float64 {
	return min + g.rng.Float64()*(max-min)
}

// forceDifferent avoids consecutive duplicate values, especially for booleans.
func (g *ValueGenerator) forceDifferent(current any) any {
	switch g.dataType {
	case protocol.DataMotion:
		if v, ok := current.(bool); ok {
			return !v
		}
		return g.generate()
	default:
		value, ok := current.(float64)
		if !ok {
			return g.generate()
		}
		// Nudge within range to ensure a different value.
		return value + 0.01
	}
}

// valuesEqual compares only the supported payload types.
func valuesEqual(a, b any) bool {
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	default:
		return false
	}
}
