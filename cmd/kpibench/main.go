// kpibench records measurements, never substitutes HTTP RTT for alert delivery.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pq-heal/internal/client"
	"pq-heal/internal/secure"
	"pq-heal/internal/server"
)

type distribution struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	Max  float64 `json:"max_ms"`
}

func summarize(v []float64) distribution {
	if len(v) == 0 {
		return distribution{}
	}
	v = append([]float64(nil), v...)
	sort.Float64s(v)
	d := distribution{N: len(v), P50: v[(len(v)-1)*50/100], P95: v[(len(v)-1)*95/100], P99: v[(len(v)-1)*99/100], Max: v[len(v)-1]}
	for _, x := range v {
		d.Mean += x
	}
	d.Mean /= float64(len(v))
	return d
}
func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func writeJSON(path string, v any) {
	b, e := json.MarshalIndent(v, "", "  ")
	must(e)
	must(os.WriteFile(path, append(b, '\n'), 0600))
}

type probe struct {
	base, key string
	http      *http.Client
	bundle    secure.ServerKeyBundle
}
type response struct {
	status   int
	id, code string
	body     []byte
}

func (p *probe) request(method, path string, body []byte, auth bool) (response, error) {
	req, err := http.NewRequest(method, p.base+path, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return response{}, err
	}
	var api struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &api)
	return response{resp.StatusCode, resp.Header.Get("X-Request-ID"), api.Error.Code, b}, nil
}
func payload(id, typ string, value any, at time.Time) []byte {
	b, e := json.Marshal(map[string]any{"id": id, "subject_id": "kpi-synthetic-subject", "type": typ, "value": value, "timestamp": at.UTC()})
	must(e)
	return b
}
func (p *probe) envelope(path, id string, at time.Time, b []byte) []byte {
	env, e := secure.EncryptPayload(p.bundle, path, id, at, b)
	must(e)
	out, e := json.Marshal(env)
	must(e)
	return out
}

type timedSink struct {
	mu   sync.Mutex
	byID map[string]time.Time
}

func (s *timedSink) Publish(a server.Alert) {
	s.mu.Lock()
	s.byID[a.RequestID] = time.Now()
	s.mu.Unlock()
}
func (s *timedSink) take(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.byID[id]
	delete(s.byID, id)
	return at, ok
}

func verify(p *probe, out string) bool {
	checks := []map[string]any{}
	all := true
	check := func(name, method, path string, body []byte, auth bool, want int) {
		r, e := p.request(method, path, body, auth)
		ok := e == nil && r.status == want && r.id != ""
		expectedCode := map[string]string{"replay_rejected": "replay_detected", "tamper_rejected": "decryption_failed", "stale_timestamp_rejected": "timestamp_too_old", "null_value_rejected": "value_required"}[name]
		if expectedCode != "" {
			ok = ok && r.code == expectedCode
		}
		all = all && ok
		item := map[string]any{"check": name, "pass": ok, "status": r.status, "expected_status": want, "error_code": r.code, "request_id": r.id}
		if e != nil {
			item["transport_error"] = e.Error()
		}
		checks = append(checks, item)
	}
	check("health", "GET", "/health", nil, false, 200)
	check("authentication_required", "GET", "/clients", nil, false, 401)
	check("authenticated_registry", "GET", "/clients", nil, true, 200)
	for _, s := range []struct {
		typ   string
		value any
	}{{"heart_rate", 72.0}, {"person_temperature", 36.5}, {"ambient_temperature", 24.0}, {"motion", true}} {
		id := "verify-" + s.typ
		at := time.Now()
		b := p.envelope("/ingest", id, at, payload(id, s.typ, s.value, at))
		check("encrypted_"+s.typ, "POST", "/ingest", b, true, 202)
		if s.typ == "heart_rate" {
			check("replay_rejected", "POST", "/ingest", b, true, 400)
			at = time.Now()
			env, e := secure.EncryptPayload(p.bundle, "/ingest", id, at, payload(id, s.typ, s.value, at))
			must(e)
			// Flip a valid base64 ciphertext character so the authentication tag fails.
			if env.Ciphertext[0] == 'A' {
				env.Ciphertext = "B" + env.Ciphertext[1:]
			} else {
				env.Ciphertext = "A" + env.Ciphertext[1:]
			}
			b, e = json.Marshal(env)
			must(e)
			check("tamper_rejected", "POST", "/ingest", b, true, 400)
		}
	}
	id := "verify-heart_rate"
	at := time.Now()
	b, e := json.Marshal(map[string]any{"id": id, "type": "heart_rate", "status": "alive", "timestamp": at})
	must(e)
	check("encrypted_heartbeat", "POST", "/heartbeat", p.envelope("/heartbeat", id, at, b), true, 202)
	at = time.Now().Add(-5 * time.Minute)
	check("stale_timestamp_rejected", "POST", "/ingest", p.envelope("/ingest", id, at, payload(id, "heart_rate", 72, at)), true, 400)
	at = time.Now()
	check("null_value_rejected", "POST", "/ingest", p.envelope("/ingest", id, at, payload(id, "heart_rate", nil, at)), true, 400)
	at = time.Now()
	check("threshold_injection_accepted_check_server_alert_log", "POST", "/ingest", p.envelope("/ingest", id, at, payload(id, "heart_rate", 165, at)), true, 202)
	writeJSON(filepath.Join(out, "verification.json"), map[string]any{"pass": all, "checks": checks, "alert_delivery": "not tested; inspect server JSON logs for threshold injection request_id"})
	return all
}

func measure(p *probe, sink *timedSink, pipe *server.DetectionPipeline, out string, n int, interval time.Duration) bool {
	f, e := os.OpenFile(filepath.Join(out, "samples.csv"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(e)
	w := csv.NewWriter(f)
	must(w.Write([]string{"sample", "mode", "utc", "type", "status", "error_code", "request_id", "prepare_ms", "http_ms", "total_ms", "alert_ms", "transport_error"}))
	samples := map[string][]float64{}
	failures, missed := 0, 0
	started := time.Now()
	for i := 0; i < n; i++ {
		// Alternate ordering to reduce drift bias; each pair has the same reading.
		modes := []string{"plaintext_https", "hybrid_https"}
		if i%2 == 1 {
			modes[0], modes[1] = modes[1], modes[0]
		}
		for _, mode := range modes {
			typ := "heart_rate"
			value := any(72.0 + float64(i%7)/10)
			if sink == nil {
				switch i % 4 {
				case 1:
					typ = "person_temperature"
					value = 36.5 + float64(i%3)/100
				case 2:
					typ = "ambient_temperature"
					value = 23.0 + float64(i%5)/10
				case 3:
					typ = "motion"
					value = i%8 == 3
				}
			} else {
				value = 165.0
			}
			id := "kpi-" + mode + "-" + typ
			start := time.Now()
			at := start.UTC()
			b := payload(id, typ, value, at)
			if mode == "hybrid_https" {
				b = p.envelope("/ingest", id, at, b)
			}
			ready := time.Now()
			r, err := p.request("POST", "/ingest", b, true)
			end := time.Now()
			total := float64(end.Sub(start)) / float64(time.Millisecond)
			httpMS := float64(end.Sub(ready)) / float64(time.Millisecond)
			alert := ""
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			if err != nil || r.status != 202 {
				failures++
			} else {
				samples[mode] = append(samples[mode], total)
				samples[mode+"_http"] = append(samples[mode+"_http"], httpMS)
				if sink != nil {
					found := false
					for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
						if t, ok := sink.take(r.id); ok {
							v := float64(t.Sub(ready)) / float64(time.Millisecond)
							samples[mode+"_local_alert"] = append(samples[mode+"_local_alert"], v)
							alert = strconv.FormatFloat(v, 'f', 6, 64)
							found = true
							break
						}
						time.Sleep(50 * time.Microsecond)
					}
					if !found {
						missed++
					}
				}
			}
			must(w.Write([]string{strconv.Itoa(i), mode, at.Format(time.RFC3339Nano), typ, strconv.Itoa(r.status), r.code, r.id, fmt.Sprintf("%.6f", float64(ready.Sub(start))/float64(time.Millisecond)), fmt.Sprintf("%.6f", httpMS), fmt.Sprintf("%.6f", total), alert, errText}))
			if interval > 0 {
				time.Sleep(interval)
			}
		}
	}
	w.Flush()
	must(w.Error())
	must(f.Close())
	distributions := map[string]distribution{}
	for k, v := range samples {
		distributions[k] = summarize(v)
	}
	a, b := distributions["plaintext_https"], distributions["hybrid_https"]
	result := map[string]any{"distributions": distributions, "failed_requests": failures, "missing_local_alerts": missed, "attempts": 2 * n, "elapsed_seconds": time.Since(started).Seconds(), "baseline": "JSON without message envelope over the same HTTPS connection; not classical-only X25519", "kpi1_full_network_alert": "NOT_MEASURED: no network receiver or slice integration", "kpi3_gdpr": "MANUAL_REVIEW_REQUIRED", "target_overhead_percent": 15, "target_alert_ms": 10, "percentiles": "sorted floor((n-1)*p); successful requests only; consult failures", "throughput_note": "paced successful requests / elapsed wall time; not saturation capacity"}
	result["successful_requests_per_second"] = float64(a.N+b.N) / time.Since(started).Seconds()
	if a.N > 0 && b.N > 0 && a.Mean > 0 {
		overhead := (b.Mean/a.Mean - 1) * 100
		result["mean_total_overhead_percent"] = overhead
		result["overhead_within_15_percent_for_this_baseline"] = overhead <= 15 && failures == 0
	}
	if pipe != nil {
		scored, alerts, dropped := pipe.Stats()
		result["detector"] = map[string]any{"scored": scored, "alerts": alerts, "dropped": dropped}
		result["local_scope"] = "in-process HTTPS handler, CSV logging enabled, direct timing sink; not deployed systemd or network alert delivery"
	}
	writeJSON(filepath.Join(out, "measurements.json"), result)
	return failures == 0 && missed == 0
}

func main() {
	mode := flag.String("mode", "remote", "remote, verify, local, or pin")
	base := flag.String("server", "", "HTTPS server URL")
	pin := flag.String("key-pin", "", "trusted message key fingerprint")
	model := flag.String("model", "", "model required for local mode")
	out := flag.String("out", "", "new output directory")
	n := flag.Int("samples", 1000, "pairs of requests (1..20000)")
	interval := flag.Duration("interval", 50*time.Millisecond, "pause after each remote request")
	flag.Parse()
	if *n < 1 || *n > 20000 || *interval < 0 {
		must(fmt.Errorf("invalid samples/interval"))
	}
	if *mode != "remote" && *mode != "verify" && *mode != "local" && *mode != "pin" {
		must(fmt.Errorf("unknown mode"))
	}
	if *mode != "pin" {
		if *out == "" {
			must(fmt.Errorf("-out is required"))
		}
		must(os.Mkdir(*out, 0700))
	}
	httpClient, err := client.NewHTTPClient(10*time.Second, os.Getenv("SSL_CERT_FILE"))
	must(err)
	p := &probe{base: strings.TrimRight(*base, "/"), key: os.Getenv("PQHEAL_API_KEY"), http: httpClient}
	var sink *timedSink
	var pipe *server.DetectionPipeline
	if *mode == "local" {
		m, e := server.LoadModel(*model)
		must(e)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sink = &timedSink{byID: map[string]time.Time{}}
		pipe = server.NewDetectionPipeline(m, sink, server.DefaultDetectionQueue)
		done := make(chan struct{})
		go func() { defer close(done); pipe.Run(ctx) }()
		defer func() { cancel(); <-done }()
		logger, e := server.NewCSVLogger(filepath.Join(*out, "local-telemetry.csv"))
		must(e)
		defer logger.Close()
		p.key = "ephemeral-local-benchmark-only"
		h, e := server.NewHandler(server.HandlerConfig{Store: server.NewStore(1000), Logger: logger, APIKey: p.key, Context: ctx, Detection: pipe})
		must(e)
		ts := httptest.NewTLSServer(h)
		defer ts.Close()
		p.base = ts.URL
		p.http = ts.Client()
		p.http.Timeout = 10 * time.Second
	} else {
		u, e := url.Parse(p.base)
		must(e)
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			must(fmt.Errorf("server must be an HTTPS origin, without path, credentials or query"))
		}
		if *mode != "pin" && p.key == "" {
			must(fmt.Errorf("PQHEAL_API_KEY is required"))
		}
	}
	r, e := p.request("GET", "/keys", nil, false)
	must(e)
	if r.status != 200 {
		must(fmt.Errorf("key endpoint status %d", r.status))
	}
	must(secure.DecodeStrictJSON(r.body, &p.bundle))
	if *mode == "pin" {
		fmt.Println(secure.KeyBundleFingerprint(p.bundle))
		return
	}
	if *mode != "local" {
		if !secure.VerifyKeyBundleFingerprint(p.bundle, *pin) {
			must(client.ErrKeyPinMismatch)
		}
	}
	// Exclude bootstrap/TLS setup from the timed runs; retain warm-up evidence.
	if *mode != "verify" {
		for i := 0; i < 10; i++ {
			at := time.Now()
			id := "kpi-warmup"
			r, e := p.request("POST", "/ingest", p.envelope("/ingest", id, at, payload(id, "heart_rate", 72+i%3, at)), true)
			must(e)
			if r.status != 202 {
				must(fmt.Errorf("warmup status=%d code=%s", r.status, r.code))
			}
		}
	}
	ok := true
	if *mode == "verify" {
		ok = verify(p, *out)
	} else {
		ok = measure(p, sink, pipe, *out, *n, *interval)
	}
	fmt.Println("Results:", *out)
	if !ok {
		os.Exit(1)
	}
}
