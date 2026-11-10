// Package detect implements the PQ-HEAL layered anomaly detection model.
//
// The model runs on the Home Hub against the validated, decrypted telemetry
// stream delivered by the protocol layer (see D2.2 Protocol Implementations
// Report). It is organised as four layers, evaluated in order of increasing
// cost, so that the cheap deterministic checks short-circuit before the
// Isolation Forest is consulted:
//
//	L1 static physiological thresholds  (population-level, D1.1 Table 3)
//	L2 stream-integrity rules           (stuck sensor, cadence/staleness)
//	L3 personalised adaptive bounds     (regime-conditional robust z-score)
//	L4 Isolation Forest                 (multivariate residual anomalies)
package detect

import (
	"math"
	"time"

	"pq-heal/internal/protocol"
)

// Feature-extraction parameters. These are fixed by the D2.2 evaluation and
// changing them invalidates a trained model.
const (
	// EWMAAlpha is the smoothing factor of the per-stream baseline. A single
	// anomalous reading is still absorbed, but slowly, so it cannot drag the
	// baseline far enough to mask the reading that follows it.
	EWMAAlpha = 0.05

	// Window is the number of readings in the short-term shift window.
	Window = 12

	// Warmup is the number of readings a stream must deliver before the
	// adaptive layers score it. Until then only L1 and L2 apply.
	Warmup = 20

	// StuckRepeats is the number of consecutive identical readings that
	// indicate a failed or replayed sensor.
	StuckRepeats = 2

	// CadenceFactor is the multiple of a stream's learned inter-arrival time
	// beyond which a gap is treated as a transmission anomaly.
	CadenceFactor = 6.0

	// NumFeatures is the dimensionality of the feature vector.
	NumFeatures = 8

	epsilon = 1e-9
)

// Integrity classifies the deterministic stream-integrity faults detected by L2.
type Integrity int

const (
	IntegrityOK Integrity = iota
	IntegrityStuck
	IntegrityCadence
)

func (i Integrity) String() string {
	switch i {
	case IntegrityStuck:
		return "stuck_sensor"
	case IntegrityCadence:
		return "cadence_gap"
	default:
		return "ok"
	}
}

// Features is the per-reading feature vector consumed by the Isolation Forest.
// The field order matches the index order used during training; do not reorder.
type Features [NumFeatures]float64

// Feature vector indices, named for readability at the call sites.
const (
	FeatValue = iota
	FeatZScore
	FeatDelta
	FeatRate
	FeatLogInterArrival
	FeatMotion
	FeatZRegime
	FeatShift
)

// ewma holds an exponentially weighted mean and variance.
type ewma struct {
	mean  float64
	vari  float64
	count int
}

func (e *ewma) update(value float64) {
	if e.count == 0 {
		e.mean = value
		e.vari = 0
		e.count = 1
		return
	}
	diff := value - e.mean
	e.mean += EWMAAlpha * diff
	e.vari = (1 - EWMAAlpha) * (e.vari + EWMAAlpha*diff*diff)
	e.count++
}

func (e *ewma) stddev() float64 { return math.Sqrt(e.vari) }

// streamState is the per-client, per-sensor-type online state. Memory is O(1)
// per stream apart from the fixed-length window ring, so total footprint is
// bounded by the Home Hub's configured client capacity.
type streamState struct {
	baseline ewma
	resting  ewma // baseline while the client reports no motion
	active   ewma // baseline during an activity bout

	lastValue float64
	lastSeen  time.Time
	repeats   int
	cadence   float64

	win    [Window]float64
	winLen int
	winPos int
}

func (s *streamState) windowMean() (float64, bool) {
	if s.winLen < Window {
		return 0, false
	}
	sum := 0.0
	for _, v := range s.win {
		sum += v
	}
	return sum / Window, true
}

func (s *streamState) pushWindow(value float64) {
	s.win[s.winPos] = value
	s.winPos = (s.winPos + 1) % Window
	if s.winLen < Window {
		s.winLen++
	}
}

// Observation is the outcome of feature extraction for a single reading.
type Observation struct {
	Features  Features
	Warm      bool      // stream has not yet passed Warmup readings
	Integrity Integrity // L2 verdict
	ZRegime   float64   // L3 statistic, retained for alert explanations
}

type streamKey struct {
	clientID string
	dataType protocol.DataType
}

// Extractor maintains online per-stream state and turns raw readings into
// feature vectors. It is not safe for concurrent use; the Home Hub serialises
// telemetry through a single detector goroutine.
type Extractor struct {
	streams map[streamKey]*streamState
	motion  map[string]float64
}

func NewExtractor() *Extractor {
	return &Extractor{
		streams: make(map[streamKey]*streamState),
		motion:  make(map[string]float64),
	}
}

// ObserveMotion records the latest motion state for a client. Motion is a
// discrete sensor and is not scored directly; it conditions the interpretation
// of the quantitative streams belonging to the same client.
func (e *Extractor) ObserveMotion(clientID string, moved bool) {
	if moved {
		e.motion[clientID] = 1
		return
	}
	e.motion[clientID] = 0
}

// Forget releases the state held for a client. The Home Hub calls this when a
// client is evicted from the store so that detector memory tracks store memory.
func (e *Extractor) Forget(clientID string) {
	delete(e.motion, clientID)
	for k := range e.streams {
		if k.clientID == clientID {
			delete(e.streams, k)
		}
	}
}

// Extract computes the feature vector for one reading and folds it into the
// stream's online state.
func (e *Extractor) Extract(clientID string, dataType protocol.DataType, value float64, at time.Time) Observation {
	key := streamKey{clientID: clientID, dataType: dataType}
	st, ok := e.streams[key]
	if !ok {
		st = &streamState{}
		e.streams[key] = st
	}
	moving := e.motion[clientID]

	var interArrival, delta float64
	if st.baseline.count > 0 {
		interArrival = at.Sub(st.lastSeen).Seconds()
		delta = value - st.lastValue
	}

	warm := st.baseline.count < Warmup

	var z float64
	if !warm {
		z = (value - st.baseline.mean) / (st.baseline.stddev() + epsilon)
	}

	var rate float64
	if interArrival > 0 {
		rate = delta / interArrival
	}

	// Regime-conditional deviation. A heart rate of 112 BPM is unremarkable
	// during an activity bout and clinically significant at rest, so the
	// baseline is maintained separately for each motion regime.
	regime := &st.resting
	if moving > 0.5 {
		regime = &st.active
	}
	var zRegime float64
	if regime.count >= Warmup {
		zRegime = (value - regime.mean) / (regime.stddev() + epsilon)
	}

	// Sustained-shift statistic. A lone spike barely moves the window mean; a
	// sustained excursion moves it a long way from the long-run baseline.
	var shift float64
	if wmean, full := st.windowMean(); full && !warm {
		shift = (wmean - st.baseline.mean) / (st.baseline.stddev() + epsilon)
	}

	if st.baseline.count > 0 && value == st.lastValue {
		st.repeats++
	} else {
		st.repeats = 0
	}

	integrity := IntegrityOK
	switch {
	case st.repeats >= StuckRepeats:
		integrity = IntegrityStuck
	case st.cadence > 0 && !warm && interArrival > CadenceFactor*st.cadence:
		integrity = IntegrityCadence
	}

	obs := Observation{
		Features: Features{
			FeatValue:           value,
			FeatZScore:          z,
			FeatDelta:           delta,
			FeatRate:            rate,
			FeatLogInterArrival: math.Log1p(math.Max(interArrival, 0)),
			FeatMotion:          moving,
			FeatZRegime:         zRegime,
			FeatShift:           shift,
		},
		Warm:      warm,
		Integrity: integrity,
		ZRegime:   zRegime,
	}

	st.baseline.update(value)
	regime.update(value)
	if st.baseline.count > 1 && interArrival > 0 {
		if st.cadence == 0 {
			st.cadence = interArrival
		} else {
			st.cadence = 0.9*st.cadence + 0.1*interArrival
		}
	}
	st.lastValue = value
	// Only ever advance the stream clock. A reading that arrives out of order
	// carries an older client timestamp; letting it pull lastSeen backwards
	// would inflate the next reading's inter-arrival time and report a cadence
	// gap that never happened.
	if at.After(st.lastSeen) {
		st.lastSeen = at
	}
	st.pushWindow(value)

	return obs
}
