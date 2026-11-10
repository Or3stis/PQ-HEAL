package detect

import (
	"math"
	"time"

	"pq-heal/internal/protocol"
)

// ZLimit is the regime-conditional z-score at or beyond which the personalised
// adaptive layer (L3) raises an alert. Selected in D2.2 Section 6.
const ZLimit = 4.5

// Layer identifies which detection layer raised an alert.
type Layer int

const (
	LayerNone Layer = iota
	LayerThreshold
	LayerIntegrity
	LayerAdaptive
	LayerForest
)

func (l Layer) String() string {
	switch l {
	case LayerThreshold:
		return "L1_threshold"
	case LayerIntegrity:
		return "L2_integrity"
	case LayerAdaptive:
		return "L3_adaptive"
	case LayerForest:
		return "L4_forest"
	default:
		return "none"
	}
}

// LayerMask selects which layers may raise an alert.
//
// It exists so that the D2.2 layer ablation can disable a layer inside the
// cascade. Filtering a finished verdict by its layer is not equivalent and is
// silently wrong: the cascade stops at the first layer that fires, so a reading
// caught by L3 never reaches L4, and discarding that verdict afterwards records
// a miss for a reading the forest would in fact have caught had L3 been absent.
type LayerMask uint8

const (
	MaskThreshold LayerMask = 1 << iota
	MaskIntegrity
	MaskAdaptive
	MaskForest

	// MaskAll is the production configuration.
	MaskAll = MaskThreshold | MaskIntegrity | MaskAdaptive | MaskForest
)

func (m LayerMask) enabled(bit LayerMask) bool { return m&bit != 0 }

// Result is the verdict for a single telemetry reading.
type Result struct {
	Anomalous bool
	Layer     Layer
	Integrity Integrity
	Score     float64 // Isolation Forest score, zero when the forest was not consulted
	ZRegime   float64
	Reason    string
}

// Detector applies the four-layer model to a live telemetry stream. It is not
// safe for concurrent use; the Home Hub serialises readings through a single
// detector goroutine, matching the ingest path's ordering guarantees.
type Detector struct {
	extractor *Extractor
	model     *Model
}

// New returns a detector backed by a trained model. A nil model is permitted
// and disables L4 only, so the hub degrades to the deterministic and adaptive
// layers rather than failing closed on a missing or corrupt model file.
func New(model *Model) *Detector {
	return &Detector{extractor: NewExtractor(), model: model}
}

// ObserveMotion forwards a discrete motion reading into the detector's context.
func (d *Detector) ObserveMotion(clientID string, moved bool) {
	d.extractor.ObserveMotion(clientID, moved)
}

// Forget releases all state held for a client.
func (d *Detector) Forget(clientID string) { d.extractor.Forget(clientID) }

// Observe scores one quantitative telemetry reading. Layers are evaluated in
// order of increasing cost and the first to fire wins, so a reading that
// violates a static threshold never reaches the forest.
func (d *Detector) Observe(clientID string, dataType protocol.DataType, value float64, at time.Time) Result {
	return d.ObserveLayers(clientID, dataType, value, at, MaskAll)
}

// ObserveLayers is Observe with a subset of the layers enabled. Production code
// calls Observe; this entry point exists for the D2.2 ablation, which must
// disable a layer within the cascade so that readings fall through to the
// layers that remain.
func (d *Detector) ObserveLayers(clientID string, dataType protocol.DataType, value float64, at time.Time, mask LayerMask) Result {
	obs := d.extractor.Extract(clientID, dataType, value, at)

	if mask.enabled(MaskThreshold) && !protocol.CheckThreshold(dataType, value) {
		return Result{
			Anomalous: true,
			Layer:     LayerThreshold,
			ZRegime:   obs.ZRegime,
			Reason:    "reading outside the configured physiological range",
		}
	}

	if mask.enabled(MaskIntegrity) && obs.Integrity != IntegrityOK {
		return Result{
			Anomalous: true,
			Layer:     LayerIntegrity,
			Integrity: obs.Integrity,
			ZRegime:   obs.ZRegime,
			Reason:    obs.Integrity.String(),
		}
	}

	// Until a stream has delivered Warmup readings its personalised baseline is
	// too weak to score against, so the adaptive layers abstain. Irregular
	// sampling, sleep periods and short disconnections therefore cannot in
	// themselves produce a false positive, as required by D1.1 Section 6.1.
	if obs.Warm {
		return Result{ZRegime: obs.ZRegime}
	}

	if mask.enabled(MaskAdaptive) && math.Abs(obs.ZRegime) >= ZLimit {
		return Result{
			Anomalous: true,
			Layer:     LayerAdaptive,
			ZRegime:   obs.ZRegime,
			Reason:    "reading deviates from the client's baseline for the current activity regime",
		}
	}

	if !mask.enabled(MaskForest) {
		return Result{ZRegime: obs.ZRegime}
	}
	forest := d.forestFor(dataType)
	if forest == nil {
		return Result{ZRegime: obs.ZRegime}
	}
	score := forest.Score(obs.Features)
	if score >= forest.Threshold {
		return Result{
			Anomalous: true,
			Layer:     LayerForest,
			Score:     score,
			ZRegime:   obs.ZRegime,
			Reason:    "multivariate isolation score above the trained decision threshold",
		}
	}
	return Result{Score: score, ZRegime: obs.ZRegime}
}

func (d *Detector) forestFor(dataType protocol.DataType) *Forest {
	if d.model == nil {
		return nil
	}
	return d.model.Forests[string(dataType)]
}
