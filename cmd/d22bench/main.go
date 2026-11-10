// Command d22b_anomaly_bench trains and evaluates the PQ-HEAL layered anomaly
// detection model and measures its inference latency on the Home Hub code path.
//
// It is the Go counterpart of the reference implementation in ref_eval.py: the
// two are independent implementations of the same model, so agreement between
// their accuracy figures is evidence that neither contains a scoring defect.
//
// Run from the repository root with the corpora in the working directory:
//
//	cp Deliverables/D2.2b_supporting/*.go     pq-heal/cmd/d22bench/main.go
//	cp Deliverables/D2.2b_supporting/ad_*.csv pq-heal/
//	cd pq-heal && go run ./cmd/d22bench
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"pq-heal/internal/detect"
	"pq-heal/internal/protocol"
)

const seed = 20260720

var quantTypes = []protocol.DataType{
	protocol.DataHeartRate,
	protocol.DataPersonTemp,
	protocol.DataAmbientTemp,
}

var className = map[int]string{
	1: "range", 2: "contextual", 3: "rate", 4: "stuck", 5: "cadence",
}

type reading struct {
	at       time.Time
	clientID string
	dataType protocol.DataType
	raw      string
	value    float64
	moved    bool
	label    int
}

func readCorpus(path string, labelled bool) []reading {
	file, err := os.Open(path)
	if err != nil {
		fatal("open %s: %v", path, err)
	}
	defer file.Close()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		fatal("read %s: %v", path, err)
	}

	out := make([]reading, 0, len(records))
	for _, rec := range records[1:] {
		at, err := time.Parse(time.RFC3339Nano, rec[0])
		if err != nil {
			fatal("timestamp %q: %v", rec[0], err)
		}
		r := reading{
			at:       at,
			clientID: rec[1],
			dataType: protocol.DataType(rec[2]),
			raw:      rec[3],
		}
		if r.dataType == protocol.DataMotion {
			r.moved = rec[3] == "moved"
		} else if r.value, err = strconv.ParseFloat(rec[3], 64); err != nil {
			fatal("value %q: %v", rec[3], err)
		}
		if labelled {
			if r.label, err = strconv.Atoi(strings.TrimSpace(rec[5])); err != nil {
				fatal("label %q: %v", rec[5], err)
			}
		}
		out = append(out, r)
	}
	return out
}

// train fits one forest per quantitative sensor type on the anomaly-free corpus.
func train(rows []reading) *detect.Model {
	ex := detect.NewExtractor()
	sets := map[protocol.DataType][]detect.Features{}
	for _, r := range rows {
		if r.dataType == protocol.DataMotion {
			ex.ObserveMotion(r.clientID, r.moved)
			continue
		}
		obs := ex.Extract(r.clientID, r.dataType, r.value, r.at)
		if !obs.Warm {
			sets[r.dataType] = append(sets[r.dataType], obs.Features)
		}
	}

	model := &detect.Model{Version: "d2.2-anomaly", Forests: map[string]*detect.Forest{}}
	for _, dt := range quantTypes {
		f, err := detect.Train(sets[dt], seed)
		if err != nil {
			fatal("train %s: %v", dt, err)
		}
		model.Forests[string(dt)] = f
		fmt.Printf("trained %-20s n=%6d  tau=%.4f\n", dt, len(sets[dt]), f.Threshold)
	}
	return model
}

type metrics struct {
	tp, fp, tn, fn int
	byLayer        map[detect.Layer]int
	fpByLayer      map[detect.Layer]int
	perClass       map[int][2]int
	eventsTotal    map[int]int
	eventsHit      map[int]int
}

func (m metrics) recall() float64 {
	return ratio(m.tp, m.tp+m.fn)
}
func (m metrics) precision() float64 { return ratio(m.tp, m.tp+m.fp) }
func (m metrics) fpr() float64       { return ratio(m.fp, m.fp+m.tn) }
func (m metrics) accuracy() float64  { return ratio(m.tp+m.tn, m.tp+m.tn+m.fp+m.fn) }

func (m metrics) eventRecall() (int, int) {
	hit, total := 0, 0
	for k, v := range m.eventsTotal {
		total += v
		hit += m.eventsHit[k]
	}
	return hit, total
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

type openEvent struct {
	label int
	last  time.Time
	hit   bool
	idx   int
}

// evaluate replays the labelled corpus through the detector. mask selects which
// layers are permitted to raise an alert, which drives the ablation table.
func evaluate(rows []reading, model *detect.Model, mask map[detect.Layer]bool) metrics {
	// The full-model row must exercise the shipped detector, since attesting to
	// that implementation is the point of this harness. Ablation rows need the
	// mask threaded through the cascade, which Detector.Observe does not expose,
	// so they fall back to the mirror in observeMasked.
	full := len(mask) == 4
	d := detect.New(model)
	ex := detect.NewExtractor()
	m := metrics{
		byLayer:     map[detect.Layer]int{},
		fpByLayer:   map[detect.Layer]int{},
		perClass:    map[int][2]int{},
		eventsTotal: map[int]int{},
		eventsHit:   map[int]int{},
	}
	open := map[string]*openEvent{}
	var events []*openEvent

	for _, r := range rows {
		if r.dataType == protocol.DataMotion {
			d.ObserveMotion(r.clientID, r.moved)
			ex.ObserveMotion(r.clientID, r.moved)
			continue
		}
		var res detect.Result
		if full {
			res = d.Observe(r.clientID, r.dataType, r.value, r.at)
		} else {
			res = observeMasked(ex, model, r, mask)
		}
		alert := res.Anomalous
		key := r.clientID + "|" + string(r.dataType)

		if r.label > 0 {
			c := m.perClass[r.label]
			c[1]++

			ev := open[key]
			if ev == nil || ev.label != r.label || r.at.Sub(ev.last) > 120*time.Second {
				ev = &openEvent{label: r.label, last: r.at}
				events = append(events, ev)
				open[key] = ev
			}
			ev.last = r.at

			if alert {
				c[0]++
				ev.hit = true
				m.tp++
				m.byLayer[res.Layer]++
			} else {
				m.fn++
			}
			m.perClass[r.label] = c
			continue
		}

		delete(open, key)
		if alert {
			m.fp++
			m.fpByLayer[res.Layer]++
		} else {
			m.tn++
		}
	}

	for _, ev := range events {
		m.eventsTotal[ev.label]++
		if ev.hit {
			m.eventsHit[ev.label]++
		}
	}
	return m
}

func allLayers() map[detect.Layer]bool {
	return map[detect.Layer]bool{
		detect.LayerThreshold: true, detect.LayerIntegrity: true,
		detect.LayerAdaptive: true, detect.LayerForest: true,
	}
}

// observeMasked mirrors detect.Detector.Observe but consults the mask *inside*
// the cascade, so a disabled layer lets the reading fall through to the layers
// behind it instead of terminating the cascade. Applying the mask to the
// finished verdict instead makes any ablation that removes a mid-cascade layer
// under-report, because readings that layer shadowed never reach the survivors.
func observeMasked(ex *detect.Extractor, model *detect.Model, r reading, mask map[detect.Layer]bool) detect.Result {
	obs := ex.Extract(r.clientID, r.dataType, r.value, r.at)

	if mask[detect.LayerThreshold] && !protocol.CheckThreshold(r.dataType, r.value) {
		return detect.Result{Anomalous: true, Layer: detect.LayerThreshold, ZRegime: obs.ZRegime}
	}
	if mask[detect.LayerIntegrity] && obs.Integrity != detect.IntegrityOK {
		return detect.Result{Anomalous: true, Layer: detect.LayerIntegrity, ZRegime: obs.ZRegime}
	}
	if obs.Warm {
		return detect.Result{ZRegime: obs.ZRegime}
	}
	if mask[detect.LayerAdaptive] && math.Abs(obs.ZRegime) >= detect.ZLimit {
		return detect.Result{Anomalous: true, Layer: detect.LayerAdaptive, ZRegime: obs.ZRegime}
	}
	if mask[detect.LayerForest] {
		if f := model.Forests[string(r.dataType)]; f != nil {
			if score := f.Score(obs.Features); score >= f.Threshold {
				return detect.Result{Anomalous: true, Layer: detect.LayerForest, Score: score, ZRegime: obs.ZRegime}
			}
		}
	}
	return detect.Result{ZRegime: obs.ZRegime}
}

func maskOf(layers ...detect.Layer) map[detect.Layer]bool {
	m := map[detect.Layer]bool{}
	for _, l := range layers {
		m[l] = true
	}
	return m
}

// benchLatency measures the per-reading cost of the full detection path on a
// warmed stream, which is the latency the Home Hub adds between telemetry
// ingest and alert emission (KPI 1).
func benchLatency(model *detect.Model, rows []reading) {
	d := detect.New(model)
	warm := make([]reading, 0, 4096)
	for _, r := range rows {
		if r.dataType == protocol.DataHeartRate && r.label == 0 {
			warm = append(warm, r)
		}
		if len(warm) == 4096 {
			break
		}
	}
	for _, r := range warm[:512] {
		d.Observe(r.clientID, r.dataType, r.value, r.at)
	}

	const iterations = 200000
	samples := make([]float64, 0, iterations)
	at := warm[len(warm)-1].at
	for i := 0; i < iterations; i++ {
		r := warm[512+i%(len(warm)-512)]
		at = at.Add(5 * time.Second)
		start := time.Now()
		d.Observe("bench-client", protocol.DataHeartRate, r.value, at)
		samples = append(samples, float64(time.Since(start).Nanoseconds()))
	}
	sort.Float64s(samples)

	mean := 0.0
	for _, s := range samples {
		mean += s
	}
	mean /= float64(len(samples))

	fmt.Println()
	fmt.Println("== Per-reading detection latency (warmed stream, full L1-L4 path) ==")
	fmt.Printf("mean=%.2fµs  p50=%.2fµs  p90=%.2fµs  p99=%.2fµs  max=%.2fµs\n",
		mean/1000, pct(samples, 50)/1000, pct(samples, 90)/1000,
		pct(samples, 99)/1000, samples[len(samples)-1]/1000)

	// Sustained throughput of the detector alone, for capacity planning.
	start := time.Now()
	for i := 0; i < iterations; i++ {
		at = at.Add(5 * time.Second)
		d.Observe("tput-client", protocol.DataHeartRate, warm[i%len(warm)].value, at)
	}
	elapsed := time.Since(start)
	fmt.Printf("sustained throughput: %.0f readings/s single-threaded\n",
		float64(iterations)/elapsed.Seconds())
}

func pct(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := int(float64(p) / 100 * float64(len(sorted)-1))
	return sorted[k]
}

func report(m metrics) {
	fmt.Println()
	fmt.Printf("readings evaluated : %d\n", m.tp+m.fp+m.tn+m.fn)
	fmt.Printf("TP=%d FP=%d TN=%d FN=%d\n", m.tp, m.fp, m.tn, m.fn)
	fmt.Printf("reading-level detection rate : %.2f%%\n", m.recall()*100)
	fmt.Printf("precision                    : %.2f%%\n", m.precision()*100)
	fmt.Printf("false positive rate          : %.3f%%\n", m.fpr()*100)
	fmt.Printf("accuracy                     : %.2f%%\n", m.accuracy()*100)

	fmt.Print("TP by layer :")
	for _, l := range []detect.Layer{detect.LayerThreshold, detect.LayerIntegrity, detect.LayerAdaptive, detect.LayerForest} {
		fmt.Printf(" %v=%d", l, m.byLayer[l])
	}
	fmt.Print("\nFP by layer :")
	for _, l := range []detect.Layer{detect.LayerThreshold, detect.LayerIntegrity, detect.LayerAdaptive, detect.LayerForest} {
		fmt.Printf(" %v=%d", l, m.fpByLayer[l])
	}
	fmt.Println()

	fmt.Println()
	for c := 1; c <= 5; c++ {
		v := m.perClass[c]
		if v[1] == 0 {
			continue
		}
		fmt.Printf("  reading-level  class %d %-12s %d/%d = %.1f%%\n",
			c, className[c], v[0], v[1], ratio(v[0], v[1])*100)
	}
	hit, total := m.eventRecall()
	fmt.Println()
	fmt.Printf("EVENT-LEVEL detection rate : %d/%d = %.2f%%\n", hit, total, ratio(hit, total)*100)
	for c := 1; c <= 5; c++ {
		if m.eventsTotal[c] == 0 {
			continue
		}
		fmt.Printf("  event-level    class %d %-12s %d/%d = %.1f%%\n",
			c, className[c], m.eventsHit[c], m.eventsTotal[c],
			ratio(m.eventsHit[c], m.eventsTotal[c])*100)
	}
}

func main() {
	trainPath := flag.String("train", "ad_train.csv", "training corpus")
	evalPath := flag.String("eval", "ad_eval.csv", "labelled evaluation corpus")
	modelIn := flag.String("model", "", "evaluate an existing model instead of training")
	modelOut := flag.String("model-out", "", "write trained model here")
	metricsOut := flag.String("json-out", "", "write evaluation metrics as JSON")
	flag.Parse()

	fmt.Println("# PQ-HEAL D2.2 anomaly detection model — training, evaluation and latency")
	fmt.Printf("# Go %s · %d trees · subsample %d · max depth %d · contamination %g · z-limit %g\n",
		strings.TrimPrefix(runtime.Version(), "go"), detect.NumTrees, detect.Subsample,
		detect.MaxDepth, detect.Contamination, detect.ZLimit)
	fmt.Println()

	trainRows := readCorpus(*trainPath, false)
	evalRows := readCorpus(*evalPath, true)
	fmt.Printf("corpus: %d training readings, %d evaluation readings\n\n",
		len(trainRows), len(evalRows))

	var model *detect.Model
	if *modelIn != "" {
		f, err := os.Open(*modelIn)
		if err != nil {
			fatal("model: %v", err)
		}
		model, err = detect.ReadModel(f)
		f.Close()
		if err != nil {
			fatal("model: %v", err)
		}
	} else {
		model = train(trainRows)
	}
	if *modelOut != "" {
		f, err := os.Create(*modelOut)
		if err != nil {
			fatal("model output: %v", err)
		}
		err = detect.WriteModel(f, model)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			fatal("model write: %v %v", err, closeErr)
		}
	}
	full := evaluate(evalRows, model, allLayers())
	report(full)
	if *metricsOut != "" {
		hit, total := full.eventRecall()
		result := map[string]any{"corpus_scope": "supplied synthetic held-out corpus; not clinical or testbed validation", "events_hit": hit, "events_total": total, "event_recall_percent": 100 * ratio(hit, total), "target_percent": 95, "synthetic_target_met": total > 0 && ratio(hit, total) >= 0.95, "reading_recall_percent": 100 * full.recall(), "false_positive_percent": 100 * full.fpr(), "precision_percent": 100 * full.precision(), "accuracy_percent": 100 * full.accuracy(), "tp": full.tp, "fp": full.fp, "tn": full.tn, "fn": full.fn}
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fatal("metrics: %v", err)
		}
		if err := os.WriteFile(*metricsOut, append(b, '\n'), 0600); err != nil {
			fatal("metrics: %v", err)
		}
	}

	fmt.Println()
	fmt.Println("Layer ablation (event-level detection / FPR / precision):")
	for _, a := range []struct {
		name string
		mask map[detect.Layer]bool
	}{
		{"L1 thresholds only       ", maskOf(detect.LayerThreshold)},
		{"L1+L2 integrity          ", maskOf(detect.LayerThreshold, detect.LayerIntegrity)},
		{"L1+L2+L3 adaptive bounds ", maskOf(detect.LayerThreshold, detect.LayerIntegrity, detect.LayerAdaptive)},
		{"L1+L2+L4 isolation forest", maskOf(detect.LayerThreshold, detect.LayerIntegrity, detect.LayerForest)},
		{"L1+L2+L3+L4 full model   ", allLayers()},
	} {
		m := evaluate(evalRows, model, a.mask)
		hit, total := m.eventRecall()
		fmt.Printf("  %s %6.2f%%   %6.3f%%   %6.2f%%\n",
			a.name, ratio(hit, total)*100, m.fpr()*100, m.precision()*100)
	}

	benchLatency(model, evalRows)

	// Serialised model size bounds what the hub must ship and load at start-up.
	tmp, err := os.CreateTemp("", "pqheal-model-*.json")
	if err == nil {
		defer os.Remove(tmp.Name())
		if err := detect.WriteModel(tmp, model); err == nil {
			if fi, err := tmp.Stat(); err == nil {
				fmt.Printf("serialised model size: %.1f KiB\n", float64(fi.Size())/1024)
			}
		}
		tmp.Close()
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
