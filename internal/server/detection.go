package server

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"time"

	"pq-heal/internal/detect"
	"pq-heal/internal/protocol"
)

// DefaultDetectionQueue is the number of readings buffered ahead of the
// detection goroutine.
const DefaultDetectionQueue = 1024

// observation is one reading handed from the ingest path to the detector.
type observation struct {
	generation uint64
	subjectID  string
	clientID   string
	dataType   protocol.DataType
	value      float64
	moved      bool
	unit       string
	at         time.Time
	requestID  string
}

// DetectionPipeline runs the anomaly detection model off the request path.
//
// Ingest submits readings to a bounded queue and returns; a single goroutine
// drains that queue and scores each reading. The detector holds per-stream
// state and is not safe for concurrent use, so serialising through one
// goroutine is what makes it correct as well as what keeps request latency
// independent of detection cost.
type DetectionPipeline struct {
	in   chan observation
	det  *detect.Detector
	sink AlertSink

	// Eviction notifications coalesce: retained detector state is reconciled
	// against live store generations, rather than buffering unbounded IDs.
	store        atomic.Pointer[Store]
	evictionWake chan struct{}
	revision     uint64
	tracked      map[string]uint64
	subjects     map[string]string
	motions      map[string]observation

	dropped atomic.Uint64
	scored  atomic.Uint64
	alerts  atomic.Uint64
}

// NewDetectionPipeline builds a pipeline around a trained model. A nil model is
// permitted and disables the Isolation Forest layer only, so the hub continues
// to apply the threshold, stream-integrity and adaptive layers.
func NewDetectionPipeline(model *detect.Model, sink AlertSink, queue int) *DetectionPipeline {
	if queue <= 0 {
		queue = DefaultDetectionQueue
	}
	if sink == nil {
		sink = LogSink{}
	}
	return &DetectionPipeline{
		in:           make(chan observation, queue),
		det:          detect.New(model),
		sink:         sink,
		evictionWake: make(chan struct{}, 1),
		tracked:      make(map[string]uint64),
		subjects:     make(map[string]string),
		motions:      make(map[string]observation),
	}
}

// Run drains the queue until the context is cancelled. It must be started
// before the HTTP server begins accepting traffic.
func (p *DetectionPipeline) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.evictionWake:
			p.reconcile()
		case obs := <-p.in:
			p.score(obs)
		}
	}
}

// bindStore installs lifecycle notifications before the handler accepts traffic.
func (p *DetectionPipeline) bindStore(store *Store) {
	p.store.Store(store)
	store.mu.Lock()
	store.onEviction = func() {
		select {
		case p.evictionWake <- struct{}{}:
		default:
		}
	}
	store.mu.Unlock()
}

// reconcile runs only on the detector goroutine. No detector work holds the
// store lock, so scoring never blocks ingest. At most one in-flight score may
// outlive eviction; the next reconciliation releases it.
func (p *DetectionPipeline) reconcile() {
	store := p.store.Load()
	if store == nil {
		return
	}
	store.mu.RLock()
	if p.revision == store.evictionRevision {
		store.mu.RUnlock()
		return
	}
	p.revision = store.evictionRevision
	var stale []string
	for id, generation := range p.tracked {
		if store.items[id].generation != generation {
			stale = append(stale, id)
		}
	}
	store.mu.RUnlock()
	for _, id := range stale {
		p.forget(id)
	}
}

func (p *DetectionPipeline) forget(id string) {
	p.det.Forget(id)
	delete(p.tracked, id)
	delete(p.subjects, id)
	delete(p.motions, id)
}

func (p *DetectionPipeline) score(obs observation) {
	p.reconcile()
	if store := p.store.Load(); store != nil {
		if obs.generation == 0 || store.generation(obs.clientID) != obs.generation {
			p.dropped.Add(1)
			return
		}
	}
	subject := obs.subjectID
	if subject == "" {
		subject = obs.clientID
	}
	if old, ok := p.tracked[obs.clientID]; ok && (old != obs.generation || p.subjects[obs.clientID] != subject) {
		p.forget(obs.clientID)
	}
	p.tracked[obs.clientID] = obs.generation
	p.subjects[obs.clientID] = subject
	obs.subjectID = subject
	// Motion is discrete and is never anomalous alone. It conditions the
	// interpretation of the client's quantitative streams.
	if obs.dataType == protocol.DataMotion {
		previous, ok := p.motions[obs.clientID]
		if !ok || !obs.at.Before(previous.at) {
			p.motions[obs.clientID] = obs
		}
		return
	}

	// Baselines stay per sensor; only activity context is shared by subject.
	// The latest timestamp wins if several motion sensors report for a subject.
	var latest observation
	for _, motion := range p.motions {
		if motion.subjectID == subject && (latest.at.IsZero() || motion.at.After(latest.at) || (motion.at.Equal(latest.at) && motion.clientID < latest.clientID)) {
			latest = motion
		}
	}
	p.det.ObserveMotion(obs.clientID, latest.moved)
	res := p.det.Observe(obs.clientID, obs.dataType, obs.value, obs.at)
	p.scored.Add(1)
	if !res.Anomalous {
		return
	}
	p.alerts.Add(1)

	// A static threshold breach is reported as such rather than as a generic
	// anomaly, because D2.2 Section 4.4 defines it as a distinct protocol-level
	// event and operators escalate the two differently.
	if res.Layer == detect.LayerThreshold {
		a := thresholdAlert(obs.clientID, obs.dataType, obs.value, obs.unit, obs.at, obs.requestID)
		p.sink.Publish(a)
		return
	}

	p.sink.Publish(Alert{
		Kind:      AlertAnomaly,
		Priority:  classifyPriority(AlertAnomaly, obs.dataType),
		ClientID:  obs.clientID,
		Type:      obs.dataType,
		At:        obs.at,
		RequestID: obs.requestID,
		Value:     obs.value,
		Unit:      obs.unit,
		HasValue:  true,
		Layer:     res.Layer.String(),
		Reason:    res.Reason,
		Score:     res.Score,
	})
}

// Submit queues a validated reading for scoring and reports whether it was
// accepted.
//
// The send is non-blocking. Under a burst that outpaces the detector the
// reading is dropped and counted, which degrades detection coverage but never
// delays ingest or applies backpressure to a sensor client. Telemetry itself is
// already durable at this point, having been stored and logged.
func (p *DetectionPipeline) Submit(r Reading, requestID string) bool {
	var generation uint64
	if store := p.store.Load(); store != nil {
		generation = store.generation(r.ID)
	}
	return p.submit(r, requestID, generation)
}

func (p *DetectionPipeline) submit(r Reading, requestID string, generation uint64) bool {
	obs := observation{
		generation: generation,
		subjectID:  r.SubjectID,
		clientID:   r.ID,
		dataType:   r.Type,
		value:      r.Numeric,
		moved:      r.Numeric != 0,
		unit:       r.Unit,
		at:         r.DataAt,
		requestID:  requestID,
	}

	select {
	case p.in <- obs:
		return true
	default:
		p.dropped.Add(1)
		return false
	}
}

// Stats reports pipeline counters. Dropped includes queue overflow and queued
// observations whose client registration was evicted before scoring.
func (p *DetectionPipeline) Stats() (scored, alerts, dropped uint64) {
	return p.scored.Load(), p.alerts.Load(), p.dropped.Load()
}

// WatchLiveness emits a liveness-loss alert the first time a client exceeds its
// alive window, and re-arms once the client reports again.
//
// This is the absence half of the alerting contract in D2.2 Section 4.4. It is
// distinct from the cadence-gap layer of the detection model, which observes a
// gap only once transmission resumes; a client that never returns is visible
// here and nowhere else.
func (p *DetectionPipeline) WatchLiveness(ctx context.Context, store *Store, aliveWindow time.Duration, interval time.Duration) {
	if aliveWindow <= 0 {
		return
	}
	if interval <= 0 {
		interval = aliveWindow / 2
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	alerted := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now()
		present := make(map[string]bool)
		for _, rec := range store.Snapshot() {
			present[rec.ID] = true
			if rec.LastSeen.IsZero() {
				continue
			}
			stale := now.Sub(rec.LastSeen) > aliveWindow
			switch {
			case stale && !alerted[rec.ID]:
				alerted[rec.ID] = true
				p.alerts.Add(1)
				p.sink.Publish(livenessAlert(rec, now))
			case !stale && alerted[rec.ID]:
				delete(alerted, rec.ID)
			}
		}
		// Drop bookkeeping for clients the store has evicted so this map cannot
		// outgrow the store it mirrors.
		for id := range alerted {
			if !present[id] {
				delete(alerted, id)
			}
		}
	}
}

// LoadModel reads a serialised anomaly detection model from disk.
func LoadModel(path string) (*detect.Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return detect.ReadModel(f)
}

// ModelSummary describes a loaded model for start-up logging.
func ModelSummary(m *detect.Model) string {
	if m == nil {
		return "none"
	}
	type entry struct {
		Type  string `json:"type"`
		Trees int    `json:"trees"`
	}
	entries := make([]entry, 0, len(m.Forests))
	for t, f := range m.Forests {
		entries = append(entries, entry{Type: t, Trees: len(f.Trees)})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "unreadable"
	}
	return string(b)
}
