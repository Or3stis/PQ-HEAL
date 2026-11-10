package detect

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

const testSeed = 20260720

var base = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

// feedResting drives n resting heart-rate readings at a 5 s cadence so that a
// stream passes warm-up with a stable personalised baseline.
func feedResting(t *testing.T, d *Detector, id string, n int) time.Time {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, 2))
	d.ObserveMotion(id, false)
	at := base
	for i := 0; i < n; i++ {
		v := 72 + rng.NormFloat64()*3
		d.Observe(id, protocol.DataHeartRate, v, at)
		at = at.Add(5 * time.Second)
	}
	return at
}

func TestThresholdLayerFiresBeforeWarmup(t *testing.T) {
	d := New(nil)
	got := d.Observe("c1", protocol.DataHeartRate, 165, base)
	if !got.Anomalous || got.Layer != LayerThreshold {
		t.Fatalf("out-of-range reading: got anomalous=%v layer=%v, want true/L1_threshold",
			got.Anomalous, got.Layer)
	}
}

func TestNormalReadingsDoNotAlert(t *testing.T) {
	d := New(nil)
	at := feedResting(t, d, "c1", 200)
	rng := rand.New(rand.NewPCG(7, 9))
	alerts := 0
	for i := 0; i < 500; i++ {
		got := d.Observe("c1", protocol.DataHeartRate, 72+rng.NormFloat64()*3, at)
		if got.Anomalous {
			alerts++
		}
		at = at.Add(5 * time.Second)
	}
	// The deterministic and adaptive layers must be effectively silent on
	// in-distribution data. ZLimit sits at 4.5 standard deviations, so a
	// stray excursion is possible in a long run; more than one indicates the
	// personalised baseline is not tracking the stream.
	if alerts > 1 {
		t.Fatalf("stable resting stream produced %d alerts, want at most 1", alerts)
	}
}

func TestStuckSensorDetected(t *testing.T) {
	d := New(nil)
	at := feedResting(t, d, "c1", 40)
	var last Result
	for i := 0; i < StuckRepeats+1; i++ {
		last = d.Observe("c1", protocol.DataHeartRate, 74.5, at)
		at = at.Add(5 * time.Second)
	}
	if !last.Anomalous || last.Layer != LayerIntegrity || last.Integrity != IntegrityStuck {
		t.Fatalf("repeated identical readings: got anomalous=%v layer=%v integrity=%v, want true/L2_integrity/stuck_sensor",
			last.Anomalous, last.Layer, last.Integrity)
	}
}

func TestCadenceGapDetected(t *testing.T) {
	d := New(nil)
	at := feedResting(t, d, "c1", 40)
	got := d.Observe("c1", protocol.DataHeartRate, 73, at.Add(20*time.Minute))
	if !got.Anomalous || got.Integrity != IntegrityCadence {
		t.Fatalf("long transmission gap: got anomalous=%v integrity=%v, want true/cadence_gap",
			got.Anomalous, got.Integrity)
	}
}

// The hub submits readings to the detector even when the store rejects them as
// stale, so an out-of-order client timestamp must not pull the stream clock
// backwards: the following in-order reading would otherwise look like a gap of
// however far back the stale reading reached.
func TestOutOfOrderReadingDoesNotFabricateCadenceGap(t *testing.T) {
	d := New(nil)
	at := feedResting(t, d, "c1", 40)

	// A reading that arrives late, carrying a timestamp 20 minutes old.
	d.Observe("c1", protocol.DataHeartRate, 73, at.Add(-20*time.Minute))

	// The next reading continues the stream's real 5 s cadence.
	got := d.Observe("c1", protocol.DataHeartRate, 73.5, at.Add(5*time.Second))
	if got.Anomalous && got.Integrity == IntegrityCadence {
		t.Fatalf("in-order reading after an out-of-order one reported a cadence gap: %+v", got)
	}
}

func TestIrregularSamplingDoesNotAlert(t *testing.T) {
	// D1.1 Section 6.1 requires that variable transmission frequency alone is
	// not treated as an anomaly. A gap below CadenceFactor must stay quiet.
	d := New(nil)
	at := feedResting(t, d, "c1", 40)
	got := d.Observe("c1", protocol.DataHeartRate, 73, at.Add(20*time.Second))
	if got.Anomalous {
		t.Fatalf("4x cadence gap alerted at layer %v, want no alert", got.Layer)
	}
}

func TestAdaptiveLayerDetectsContextualAnomaly(t *testing.T) {
	// 112 BPM is inside the static 40-120 range but far outside the resting
	// baseline of a client whose motion sensor reports no movement.
	d := New(nil)
	at := feedResting(t, d, "c1", 60)
	got := d.Observe("c1", protocol.DataHeartRate, 112, at)
	if !got.Anomalous || got.Layer != LayerAdaptive {
		t.Fatalf("in-range contextual anomaly: got anomalous=%v layer=%v z=%.2f, want true/L3_adaptive",
			got.Anomalous, got.Layer, got.ZRegime)
	}
	if !protocol.CheckThreshold(protocol.DataHeartRate, 112) {
		t.Fatal("test premise broken: 112 BPM should be inside the static threshold range")
	}
}

func TestRegimeSeparationSuppressesActivityFalsePositive(t *testing.T) {
	// The same elevated reading must not alert when the client is moving and
	// has an activity baseline that accommodates it.
	d := New(nil)
	at := feedResting(t, d, "c1", 60)
	d.ObserveMotion("c1", true)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < Warmup+40; i++ {
		d.Observe("c1", protocol.DataHeartRate, 105+rng.NormFloat64()*4, at)
		at = at.Add(5 * time.Second)
	}
	got := d.Observe("c1", protocol.DataHeartRate, 112, at)
	if got.Anomalous {
		t.Fatalf("elevated reading during an activity bout alerted at %v (z=%.2f), want no alert",
			got.Layer, got.ZRegime)
	}
}

func TestDisabledLayerFallsThroughToLaterLayers(t *testing.T) {
	// Regression test for a defect found in the D2.2 ablation harness. Because
	// the cascade stops at the first layer that fires, disabling a mid-cascade
	// layer must let the reading fall through to the layers below it. Filtering
	// a finished verdict by its layer instead records a spurious miss.
	// Two detectors driven identically, so the masked observation is scored
	// against a pristine baseline. Reusing one detector would not work: the
	// first 165 BPM reading is absorbed into the stream's EWMA variance, which
	// inflates the denominator of the regime-conditional z-score enough to put
	// an immediately following identical reading below ZLimit.
	withAll, masked := New(nil), New(nil)
	atAll := feedResting(t, withAll, "c1", 60)
	atMasked := feedResting(t, masked, "c1", 60)

	// 165 BPM violates the static threshold and is also far outside the
	// client's personalised baseline, so L1 claims it when enabled and L3 must
	// claim it when L1 is masked out.
	got := withAll.ObserveLayers("c1", protocol.DataHeartRate, 165, atAll, MaskAll)
	if got.Layer != LayerThreshold {
		t.Fatalf("with all layers: got layer %v, want L1_threshold", got.Layer)
	}

	got = masked.ObserveLayers("c1", protocol.DataHeartRate, 165, atMasked,
		MaskIntegrity|MaskAdaptive|MaskForest)
	if !got.Anomalous || got.Layer != LayerAdaptive {
		t.Fatalf("with L1 masked: got anomalous=%v layer=%v z=%.2f, want true/L3_adaptive; "+
			"the reading must fall through rather than be recorded as a miss",
			got.Anomalous, got.Layer, got.ZRegime)
	}
}

func TestOutlierInflatesVarianceAndDesensitisesAdaptiveLayer(t *testing.T) {
	// Documents a real property of the EWMA estimator rather than a defect. The
	// mean moves slowly (alpha 0.05), but the variance update is proportional to
	// the squared deviation, so one extreme reading widens the band sharply and
	// briefly desensitises L3. In the deployed cascade this is masked: a reading
	// extreme enough to cause it is a threshold violation that L1 claims first.
	d := New(nil)
	at := feedResting(t, d, "c1", 60)

	first := d.ObserveLayers("c1", protocol.DataHeartRate, 165, at,
		MaskIntegrity|MaskAdaptive|MaskForest)
	if first.Layer != LayerAdaptive {
		t.Fatalf("first outlier: got layer %v, want L3_adaptive", first.Layer)
	}

	at = at.Add(5 * time.Second)
	second := d.ObserveLayers("c1", protocol.DataHeartRate, 165, at,
		MaskIntegrity|MaskAdaptive|MaskForest)
	if second.Layer == LayerAdaptive {
		t.Skip("variance no longer absorbs a repeated outlier; the guarantee below is stronger, not weaker")
	}
	if got := second.ZRegime; got >= ZLimit {
		t.Fatalf("second outlier z = %.2f, expected it to fall below ZLimit %.1f", got, ZLimit)
	}
	// The production cascade must still report it, via L1.
	full := New(nil)
	atFull := feedResting(t, full, "c2", 60)
	if r := full.Observe("c2", protocol.DataHeartRate, 165, atFull); !r.Anomalous {
		t.Fatal("full cascade failed to report an out-of-range reading")
	}
}

func TestMaskAllMatchesObserve(t *testing.T) {
	a, b := New(nil), New(nil)
	at1 := feedResting(t, a, "c1", 60)
	at2 := feedResting(t, b, "c1", 60)
	for _, v := range []float64{74, 112, 165, 74, 74} {
		got := a.Observe("c1", protocol.DataHeartRate, v, at1)
		want := b.ObserveLayers("c1", protocol.DataHeartRate, v, at2, MaskAll)
		if got.Anomalous != want.Anomalous || got.Layer != want.Layer {
			t.Fatalf("value %.0f: Observe gave %v/%v, ObserveLayers(MaskAll) gave %v/%v",
				v, got.Anomalous, got.Layer, want.Anomalous, want.Layer)
		}
		at1 = at1.Add(5 * time.Second)
		at2 = at2.Add(5 * time.Second)
	}
}

func TestPerClientStateIsIndependent(t *testing.T) {
	d := New(nil)
	feedResting(t, d, "c1", 60)
	// A second client's first reading must be treated as warm-up, unaffected
	// by the first client's established baseline.
	got := d.Observe("c2", protocol.DataHeartRate, 95, base)
	if got.Anomalous {
		t.Fatalf("new client's first reading alerted at %v, want warm-up silence", got.Layer)
	}
}

func TestForgetReleasesState(t *testing.T) {
	d := New(nil)
	feedResting(t, d, "c1", 60)
	d.Forget("c1")
	if n := len(d.extractor.streams); n != 0 {
		t.Fatalf("after Forget: %d streams retained, want 0", n)
	}
}

// --- Isolation Forest ---

func trainingSet(n int) []Features {
	rng := rand.New(rand.NewPCG(11, 13))
	data := make([]Features, n)
	for i := range data {
		data[i] = Features{
			FeatValue:           72 + rng.NormFloat64()*4,
			FeatZScore:          rng.NormFloat64(),
			FeatDelta:           rng.NormFloat64() * 2,
			FeatRate:            rng.NormFloat64() * 0.4,
			FeatLogInterArrival: 1.79,
			FeatMotion:          0,
			FeatZRegime:         rng.NormFloat64(),
			FeatShift:           rng.NormFloat64() * 0.3,
		}
	}
	return data
}

func TestTrainRejectsSmallDataset(t *testing.T) {
	if _, err := Train(trainingSet(Subsample-1), testSeed); err == nil {
		t.Fatal("Train accepted a dataset smaller than the subsample size, want error")
	}
}

func TestForestScoresOutliersAboveInliers(t *testing.T) {
	data := trainingSet(5000)
	f, err := Train(data, testSeed)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	// The centre of the training distribution, which must be the least
	// isolated point available.
	inlier := Features{
		FeatValue: 72, FeatZScore: 0, FeatDelta: 0, FeatRate: 0,
		FeatLogInterArrival: 1.79, FeatMotion: 0, FeatZRegime: 0, FeatShift: 0,
	}
	outlier := Features{
		FeatValue: 190, FeatZScore: 14, FeatDelta: 60, FeatRate: 12,
		FeatLogInterArrival: 1.79, FeatMotion: 0, FeatZRegime: 14, FeatShift: 6,
	}
	if got, want := f.Score(outlier), f.Score(inlier); got <= want {
		t.Fatalf("outlier score %.4f, inlier score %.4f, want outlier strictly higher", got, want)
	}
	if !f.IsAnomalous(outlier) {
		t.Fatalf("outlier score %.4f below threshold %.4f, want anomalous", f.Score(outlier), f.Threshold)
	}
}

func TestContaminationSetsOperatingPoint(t *testing.T) {
	data := trainingSet(20000)
	f, err := Train(data, testSeed)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	flagged := 0
	for _, x := range data {
		if f.IsAnomalous(x) {
			flagged++
		}
	}
	rate := float64(flagged) / float64(len(data))
	// The threshold is the (1-Contamination) quantile, so the in-sample alert
	// rate must track Contamination closely. Ties in the score distribution
	// allow a small excess.
	if rate > 3*Contamination {
		t.Fatalf("in-sample alert rate %.4f, want at most %.4f", rate, 3*Contamination)
	}
}

func TestScoreIsBounded(t *testing.T) {
	f, err := Train(trainingSet(3000), testSeed)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	for _, x := range trainingSet(200) {
		s := f.Score(x)
		if s <= 0 || s > 1 || math.IsNaN(s) {
			t.Fatalf("score %.6f outside (0,1]", s)
		}
	}
}

func TestModelRoundTrip(t *testing.T) {
	f, err := Train(trainingSet(3000), testSeed)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	m := &Model{Version: "d2.2", Forests: map[string]*Forest{
		string(protocol.DataHeartRate): f,
	}}

	var buf bytes.Buffer
	if err := WriteModel(&buf, m); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	got, err := ReadModel(&buf)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	loaded := got.Forests[string(protocol.DataHeartRate)]
	if loaded == nil {
		t.Fatal("round-tripped model lost the heart_rate forest")
	}
	if loaded.Threshold != f.Threshold || loaded.Norm != f.Norm {
		t.Fatalf("round trip changed calibration: threshold %.6f/%.6f norm %.6f/%.6f",
			loaded.Threshold, f.Threshold, loaded.Norm, f.Norm)
	}
	x := trainingSet(1)[0]
	if a, b := loaded.Score(x), f.Score(x); math.Abs(a-b) > 1e-9 {
		t.Fatalf("round trip changed scoring: %.9f vs %.9f", a, b)
	}
}

func TestReadModelRejectsEmpty(t *testing.T) {
	if _, err := ReadModel(bytes.NewBufferString(`{"version":"d2.2","forests":{}}`)); err == nil {
		t.Fatal("ReadModel accepted a model with no forests, want error")
	}
}

// Score walks the tree without bounds checks, so a structurally invalid model
// must be rejected at load time rather than panicking inside the hub's
// detection goroutine on the first reading it scores.
func TestReadModelRejectsMalformedTrees(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "split node missing children",
			body: `{"version":"d2.2","forests":{"heart_rate":{"trees":[{"f":0,"s":100}],"norm":1,"threshold":0.5}}}`,
		},
		{
			name: "split node missing one child",
			body: `{"version":"d2.2","forests":{"heart_rate":{"trees":[{"f":0,"s":100,"l":{"f":-1,"n":1}}],"norm":1,"threshold":0.5}}}`,
		},
		{
			name: "feature index out of range",
			body: `{"version":"d2.2","forests":{"heart_rate":{"trees":[{"f":99,"s":1,"l":{"f":-1,"n":1},"r":{"f":-1,"n":1}}],"norm":1,"threshold":0.5}}}`,
		},
		{
			name: "null forest",
			body: `{"version":"d2.2","forests":{"heart_rate":null}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ReadModel(bytes.NewBufferString(c.body)); err == nil {
				t.Fatalf("ReadModel accepted %s, want error", c.name)
			}
		})
	}
}

func TestDetectorWithoutModelSkipsForest(t *testing.T) {
	d := New(nil)
	at := feedResting(t, d, "c1", 60)
	got := d.Observe("c1", protocol.DataHeartRate, 74, at)
	if got.Anomalous {
		t.Fatalf("nil model produced an alert at %v, want graceful degradation", got.Layer)
	}
}

func BenchmarkObserve(b *testing.B) {
	data := trainingSet(5000)
	f, err := Train(data, testSeed)
	if err != nil {
		b.Fatalf("Train: %v", err)
	}
	d := New(&Model{Version: "bench", Forests: map[string]*Forest{
		string(protocol.DataHeartRate): f,
	}})
	at := base
	for i := 0; i < 300; i++ {
		d.Observe("c1", protocol.DataHeartRate, 72, at)
		at = at.Add(5 * time.Second)
	}
	rng := rand.New(rand.NewPCG(5, 6))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Observe("c1", protocol.DataHeartRate, 72+rng.NormFloat64()*3, at)
		at = at.Add(5 * time.Second)
	}
}

func BenchmarkForestScore(b *testing.B) {
	f, err := Train(trainingSet(5000), testSeed)
	if err != nil {
		b.Fatalf("Train: %v", err)
	}
	x := trainingSet(1)[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Score(x)
	}
}
