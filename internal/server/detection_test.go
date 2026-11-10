package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

// hrReading builds a validated heart-rate reading as the ingest handler would.
func hrReading(id string, v float64, at time.Time) Reading {
	return Reading{
		ID:      id,
		Type:    protocol.DataHeartRate,
		Display: fmt.Sprintf("%.2f", v),
		Numeric: v,
		Unit:    "BPM",
		DataAt:  at,
	}
}

// motionReading builds a validated motion reading, which the detector treats as
// context for a client's quantitative streams rather than scoring it directly.
func motionReading(id string, moved bool, at time.Time) Reading {
	r := Reading{ID: id, Type: protocol.DataMotion, Display: "no motion", Unit: "Movement", DataAt: at}
	if moved {
		r.Display, r.Numeric = "motion", 1
	}
	return r
}

// captureSink collects alerts for assertions.
type captureSink struct {
	mu     sync.Mutex
	alerts []Alert
}

func (c *captureSink) Publish(a Alert) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts = append(c.alerts, a)
}

func (c *captureSink) snapshot() []Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Alert, len(c.alerts))
	copy(out, c.alerts)
	return out
}

// waitFor polls until cond holds or the deadline passes. The detection pipeline
// is asynchronous by design, so tests synchronise on its output rather than
// assuming it has run.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func startPipeline(t *testing.T) (*DetectionPipeline, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	p := NewDetectionPipeline(nil, sink, 64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go p.Run(ctx)
	return p, sink
}

func TestThresholdBreachRaisesAlert(t *testing.T) {
	p, sink := startPipeline(t)
	at := time.Now().UTC()

	p.Submit(hrReading("c1", 165, at), "req-1")

	if !waitFor(t, func() bool { return len(sink.snapshot()) > 0 }) {
		t.Fatal("no alert published for an out-of-range reading")
	}
	a := sink.snapshot()[0]
	if a.Kind != AlertThresholdBreach {
		t.Fatalf("kind = %q, want %q", a.Kind, AlertThresholdBreach)
	}
	if a.Priority != PriorityEmergency {
		t.Fatalf("priority = %q, want %q for a vital-sign breach", a.Priority, PriorityEmergency)
	}
	// D2.2 Section 4.4 requires the observed value, the breached threshold and
	// the originating request id to travel with the event.
	if a.Value != 165 || a.ThresholdMin != 40 || a.ThresholdMax != 120 {
		t.Fatalf("value/threshold = %.2f/[%.0f,%.0f], want 165.00/[40,120]",
			a.Value, a.ThresholdMin, a.ThresholdMax)
	}
	if a.RequestID != "req-1" || a.ClientID != "c1" {
		t.Fatalf("request_id/client_id = %q/%q, want req-1/c1", a.RequestID, a.ClientID)
	}
}

func TestNormalReadingRaisesNoAlert(t *testing.T) {
	p, sink := startPipeline(t)
	at := time.Now().UTC()
	for i := 0; i < 50; i++ {
		p.Submit(hrReading("c1", 72, at), "req")
		at = at.Add(5 * time.Second)
	}
	if !waitFor(t, func() bool { s, _, _ := p.Stats(); return s >= 50 }) {
		t.Fatal("pipeline did not score the submitted readings")
	}
	// A constant stream trips the stuck-sensor rule, which is correct; what
	// must not appear is a threshold breach on an in-range value.
	for _, a := range sink.snapshot() {
		if a.Kind == AlertThresholdBreach {
			t.Fatalf("in-range reading produced a threshold breach: %+v", a)
		}
	}
}

func TestStuckSensorRaisesAnomalyAlert(t *testing.T) {
	p, sink := startPipeline(t)
	at := time.Now().UTC()
	for i := 0; i < 6; i++ {
		p.Submit(hrReading("c1", 74.5, at), "req")
		at = at.Add(5 * time.Second)
	}
	ok := waitFor(t, func() bool {
		for _, a := range sink.snapshot() {
			if a.Kind == AlertAnomaly && a.Layer == "L2_integrity" {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("repeated identical readings raised no integrity alert; got %+v", sink.snapshot())
	}
}

func TestMotionUpdatesContextWithoutAlerting(t *testing.T) {
	p, sink := startPipeline(t)
	at := time.Now().UTC()
	p.Submit(motionReading("c1", true, at), "req")
	p.Submit(motionReading("c1", false, at.Add(time.Second)), "req")

	if !waitFor(t, func() bool { return true }) {
		t.Fatal("unreachable")
	}
	time.Sleep(20 * time.Millisecond)
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("motion readings produced %d alerts, want 0: %+v", len(got), got)
	}
	// Motion is context, not a scored stream.
	if scored, _, _ := p.Stats(); scored != 0 {
		t.Fatalf("scored = %d, want 0; motion must not be scored directly", scored)
	}
}

func TestAlertRequestIDMatchesResponseHeader(t *testing.T) {
	// Regression test. The handler mints a random request identifier when the
	// client supplies none, so the alert must reuse the value already written to
	// the response header. Regenerating it produces an identifier that matches
	// neither the response nor the access log, which breaks the correlation
	// D2.2 Section 4.4 requires and is invisible without an end-to-end check.
	sink := &captureSink{}
	pipe := NewDetectionPipeline(nil, sink, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pipe.Run(ctx)

	handler, err := NewHandler(HandlerConfig{
		Store:     NewStore(10),
		Context:   ctx,
		Detection: pipe,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	payload := `{"id":"c1","type":"heart_rate","value":165,"unit":"BPM","timestamp":"` +
		time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	resp, err := http.Post(srv.URL+"/ingest", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	headerID := resp.Header.Get("X-Request-ID")
	if headerID == "" {
		t.Fatal("response carried no X-Request-ID")
	}

	if !waitFor(t, func() bool { return len(sink.snapshot()) > 0 }) {
		t.Fatal("out-of-range reading produced no alert")
	}
	if got := sink.snapshot()[0].RequestID; got != headerID {
		t.Fatalf("alert request_id = %q, response header = %q; they must match "+
			"or an alert cannot be traced back to the request that caused it", got, headerID)
	}
}

// The CSV log and dashboard show a reading rounded to two decimals, but the
// detector must score what the client actually sent: rounding first would score
// a different reading than the model was trained on and make a sensor drifting
// below the second decimal place look stuck.
func TestDetectorScoresUnroundedValue(t *testing.T) {
	sink := &captureSink{}
	pipe := NewDetectionPipeline(nil, sink, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pipe.Run(ctx)

	store := NewStore(10)
	handler, err := NewHandler(HandlerConfig{Store: store, Context: ctx, Detection: pipe})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Out of range, so it raises a threshold breach carrying the value.
	payload := `{"id":"c1","type":"heart_rate","value":165.456,"unit":"BPM","timestamp":"` +
		time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	resp, err := http.Post(srv.URL+"/ingest", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	if !waitFor(t, func() bool { return len(sink.snapshot()) > 0 }) {
		t.Fatal("out-of-range reading produced no alert")
	}
	if got := sink.snapshot()[0].Value; got != 165.456 {
		t.Fatalf("alert value = %v, want 165.456 unrounded", got)
	}
	// The stored/logged form stays rounded for display.
	if got := store.Snapshot()[0].Value; got != "165.46" {
		t.Fatalf("stored value = %q, want %q", got, "165.46")
	}
}

func TestSubmitDropsRatherThanBlocking(t *testing.T) {
	// A pipeline that is never drained must not block the caller.
	sink := &captureSink{}
	p := NewDetectionPipeline(nil, sink, 4)
	at := time.Now().UTC()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.Submit(hrReading("c1", 72, at), "req")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit blocked when the detection queue was full; ingest must never stall")
	}
	if _, _, dropped := p.Stats(); dropped == 0 {
		t.Fatal("expected dropped readings to be counted when the queue overflows")
	}
}

func TestLivenessLossAlertsOncePerTransition(t *testing.T) {
	sink := &captureSink{}
	p := NewDetectionPipeline(nil, sink, 16)
	store := NewStore(10)
	if err := store.UpsertData("c1", protocol.DataHeartRate, "72.00", "BPM",
		time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.WatchLiveness(ctx, store, 50*time.Millisecond, 10*time.Millisecond)

	if !waitFor(t, func() bool { return len(sink.snapshot()) > 0 }) {
		t.Fatal("stale client raised no liveness alert")
	}
	a := sink.snapshot()[0]
	if a.Kind != AlertLivenessLoss {
		t.Fatalf("kind = %q, want %q", a.Kind, AlertLivenessLoss)
	}
	if a.HasValue {
		t.Fatal("liveness alert carried a value; it is triggered by absence, not data")
	}
	if a.LastSeen.IsZero() {
		t.Fatal("liveness alert did not carry last_seen, which D2.2 Section 4.4 requires")
	}

	// The alert is a transition, so continued staleness must not repeat it.
	time.Sleep(100 * time.Millisecond)
	count := 0
	for _, x := range sink.snapshot() {
		if x.Kind == AlertLivenessLoss {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("liveness alerts = %d, want exactly 1 per transition", count)
	}
}

func TestLivenessRearmsAfterClientReturns(t *testing.T) {
	sink := &captureSink{}
	p := NewDetectionPipeline(nil, sink, 16)
	store := NewStore(10)
	old := time.Now().Add(-time.Hour)
	if err := store.UpsertData("c1", protocol.DataHeartRate, "72.00", "BPM", old, old); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.WatchLiveness(ctx, store, 50*time.Millisecond, 5*time.Millisecond)

	countLiveness := func() int {
		n := 0
		for _, a := range sink.snapshot() {
			if a.Kind == AlertLivenessLoss {
				n++
			}
		}
		return n
	}
	if !waitFor(t, func() bool { return countLiveness() == 1 }) {
		t.Fatal("no initial liveness alert")
	}

	// Client returns, then goes stale again: a second alert is expected.
	now := time.Now()
	if err := store.UpsertData("c1", protocol.DataHeartRate, "73.00", "BPM", now, now); err != nil {
		t.Fatalf("refresh store: %v", err)
	}
	if !waitFor(t, func() bool { return countLiveness() == 2 }) {
		t.Fatalf("liveness alert did not re-arm after the client returned; count = %d", countLiveness())
	}
}

func TestAlertBusDropsRatherThanBlocking(t *testing.T) {
	bus := NewAlertBus(2)
	for i := 0; i < 50; i++ {
		bus.Publish(Alert{Kind: AlertAnomaly, ClientID: "c1"})
	}
	if bus.Dropped() == 0 {
		t.Fatal("expected undelivered alerts to be counted when no consumer drains the bus")
	}
	if got := len(bus.Alerts()); got != 2 {
		t.Fatalf("buffered alerts = %d, want 2", got)
	}
}

func TestPriorityClassification(t *testing.T) {
	cases := []struct {
		kind AlertKind
		typ  protocol.DataType
		want Priority
	}{
		{AlertThresholdBreach, protocol.DataHeartRate, PriorityEmergency},
		{AlertAnomaly, protocol.DataPersonTemp, PriorityEmergency},
		{AlertLivenessLoss, protocol.DataHeartRate, PriorityEmergency},
		{AlertAnomaly, protocol.DataAmbientTemp, PriorityRoutine},
		{AlertLivenessLoss, protocol.DataAmbientTemp, PriorityRoutine},
	}
	for _, c := range cases {
		if got := classifyPriority(c.kind, c.typ); got != c.want {
			t.Errorf("classifyPriority(%q, %q) = %q, want %q", c.kind, c.typ, got, c.want)
		}
	}
}

// Exercise the real ingest path while controlling when the queue is drained.
func TestCapacityEvictionDiscardsQueuedOldGenerations(t *testing.T) {
	store := NewStore(1)
	pipe := NewDetectionPipeline(nil, &captureSink{}, 2048)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		postReviewReading(t, handler, fmt.Sprintf("sensor-%d", i), "", "heart_rate", "72", time.Now())
	}
	for len(pipe.in) > 0 {
		pipe.score(<-pipe.in)
	}
	pipe.reconcile()
	if len(pipe.tracked) != 1 {
		t.Fatalf("retained %d clients, want 1", len(pipe.tracked))
	}
	scored, _, dropped := pipe.Stats()
	if scored != 1 || dropped != 999 {
		t.Fatalf("scored=%d dropped=%d", scored, dropped)
	}
	// Re-register the same identifier: its old queued observation must not
	// warm or contaminate the replacement stream.
	postReviewReading(t, handler, "sensor-999", "", "heart_rate", "73", time.Now())
	old := <-pipe.in
	postReviewReading(t, handler, "replacement", "", "heart_rate", "74", time.Now())
	postReviewReading(t, handler, "sensor-999", "", "heart_rate", "75", time.Now())
	pipe.score(old)
	for len(pipe.in) > 0 {
		pipe.score(<-pipe.in)
	}
	pipe.reconcile()
	scored, _, _ = pipe.Stats()
	if scored != 2 || len(pipe.tracked) != 1 {
		t.Fatalf("old registration survived: scored=%d tracked=%d", scored, len(pipe.tracked))
	}
}

func TestIdleEvictionReleasesMotionAndBaseline(t *testing.T) {
	store := NewStore(2)
	pipe := NewDetectionPipeline(nil, &captureSink{}, 8)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	postReviewReading(t, handler, "motion", "subject", "motion", "true", time.Now())
	postReviewReading(t, handler, "heart", "subject", "heart_rate", "72", time.Now())
	for len(pipe.in) > 0 {
		pipe.score(<-pipe.in)
	}
	store.mu.Lock()
	for id, rec := range store.items {
		rec.LastSeen = time.Now().Add(-time.Hour)
		store.items[id] = rec
	}
	store.mu.Unlock()
	store.Evict(time.Minute)
	select {
	case <-pipe.evictionWake:
	default:
		t.Fatal("eviction did not notify detector")
	}
	pipe.reconcile()
	if len(pipe.tracked) != 0 || len(pipe.motions) != 0 || len(pipe.subjects) != 0 {
		t.Fatal("evicted state retained")
	}
}

func postReviewReading(t *testing.T, handler http.Handler, id, subject, typ, value string, at time.Time) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"subject_id":%q,"type":%q,"value":%s,"timestamp":%q}`, id, subject, typ, value, at.UTC().Format(time.RFC3339Nano))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", response.Code, response.Body.String())
	}
}

func TestRelatedSensorsUseSeparateActivityBaselinesThroughAPI(t *testing.T) {
	store := NewStore(10)
	sink := &captureSink{}
	pipe := NewDetectionPipeline(nil, sink, 256)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	send := func(id, subject, typ, value string) {
		postReviewReading(t, handler, id, subject, typ, value, at)
		pipe.score(<-pipe.in)
		at = at.Add(100 * time.Millisecond)
	}
	send("motion", "patient", "motion", "false")
	for i := 0; i < 30; i++ {
		send("heart", "patient", "heart_rate", fmt.Sprint(69+i%3))
	}
	send("motion", "patient", "motion", "true")
	for i := 0; i < 30; i++ {
		send("heart", "patient", "heart_rate", fmt.Sprint(109+i%3))
	}
	send("heart", "patient", "heart_rate", "110")
	if got := sink.snapshot(); len(got) != 0 {
		t.Fatalf("normal activity raised alerts: %+v", got)
	}
	// Other subjects cannot change this patient's activity context.
	send("other-motion", "other", "motion", "false")
	send("heart", "patient", "heart_rate", "111")
	if len(sink.snapshot()) != 0 {
		t.Fatal("unrelated motion changed baseline")
	}
	send("motion", "patient", "motion", "false")
	send("heart", "patient", "heart_rate", "110")
	alerts := sink.snapshot()
	if len(alerts) != 1 || alerts[0].Layer != "L3_adaptive" || alerts[0].ClientID != "heart" {
		t.Fatalf("expected resting anomaly: %+v", alerts)
	}
}

func TestDetectorMemoryBoundDuringScoredClientChurn(t *testing.T) {
	store := NewStore(4)
	pipe := NewDetectionPipeline(nil, &captureSink{}, 8)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		typ, value := "heart_rate", "72"
		if i%2 == 0 {
			typ, value = "motion", "true"
		}
		postReviewReading(t, handler, fmt.Sprintf("sensor-%d", i), "patient", typ, value, time.Now())
		pipe.score(<-pipe.in)
		if len(pipe.tracked) > 4 || len(pipe.motions) > 4 || len(pipe.subjects) > 4 {
			t.Fatal("detector memory exceeded store capacity")
		}
	}
}

func TestConcurrentEvictionAndDetection(t *testing.T) {
	store := NewStore(4)
	pipe := NewDetectionPipeline(nil, &captureSink{}, 16)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); pipe.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				postReviewReading(t, handler, fmt.Sprintf("%d-%d", worker, i), "patient", "heart_rate", "72", time.Now())
				store.Evict(time.Nanosecond)
			}
		}(worker)
	}
	workers.Wait()
	if !waitFor(t, func() bool { return len(pipe.in) == 0 }) {
		t.Fatal("queue failed to drain")
	}
	cancel()
	<-done
	pipe.reconcile()
	if len(pipe.tracked) > len(store.Snapshot()) {
		t.Fatal("evicted detector state survived")
	}
}
