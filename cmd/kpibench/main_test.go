package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pq-heal/internal/secure"
	"pq-heal/internal/server"
)

func TestVerificationAgainstRealHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := server.NewHandler(server.HandlerConfig{Store: server.NewStore(100), APIKey: "test-key", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h)
	defer ts.Close()
	p := &probe{base: ts.URL, key: "test-key", http: ts.Client()}
	r, err := p.request("GET", "/keys", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = secure.DecodeStrictJSON(r.body, &p.bundle); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if !verify(p, dir) {
		b, _ := os.ReadFile(filepath.Join(dir, "verification.json"))
		t.Fatalf("verification failed: %s", b)
	}
}

func TestMeasurementsRetainRejectedRequests(t *testing.T) {
	h, err := server.NewHandler(server.HandlerConfig{Store: server.NewStore(100), APIKey: "correct"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h)
	defer ts.Close()
	p := &probe{base: ts.URL, key: "wrong", http: ts.Client()}
	r, err := p.request("GET", "/keys", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = secure.DecodeStrictJSON(r.body, &p.bundle); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if measure(p, nil, nil, dir, 2, 0) {
		t.Fatal("rejected workload must fail")
	}
	b, err := os.ReadFile(filepath.Join(dir, "measurements.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err = json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if result["failed_requests"] != float64(4) {
		t.Fatalf("missing failures: %s", b)
	}
	if _, ok := result["mean_total_overhead_percent"]; ok {
		t.Fatal("must not compute overhead without successful samples")
	}
}

func TestSummaryPrecision(t *testing.T) {
	d := summarize([]float64{0.002, 0.001, 0.003})
	if d.N != 3 || d.Mean != 0.002 || d.P50 != 0.002 || d.Max != 0.003 {
		t.Fatalf("lost sub-ms precision: %+v", d)
	}
}
