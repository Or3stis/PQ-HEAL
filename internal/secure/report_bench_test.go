package secure

import (
	"encoding/json"
	"testing"
	"time"
)

var benchPlain, _ = json.Marshal(map[string]any{
	"id":        "home-hub-ecg-01",
	"type":      "heart_rate",
	"value":     72.4,
	"unit":      "bpm",
	"timestamp": "2026-06-15T09:30:00Z",
})

func newBenchBundle(b *testing.B) (ServerKeyBundle, map[string]*KeyMaterial) {
	b.Helper()
	km, err := NewKeyMaterial("k1")
	if err != nil {
		b.Fatalf("keygen: %v", err)
	}
	return km.Bundle(), map[string]*KeyMaterial{"k1": km}
}

func BenchmarkBaselinePlaintextMarshal(b *testing.B) {
	src := map[string]any{
		"id": "home-hub-ecg-01", "type": "heart_rate",
		"value": 72.4, "unit": "bpm", "timestamp": "2026-06-15T09:30:00Z",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncryptPayload(b *testing.B) {
	bundle, _ := newBenchBundle(b)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncryptPayload(bundle, "/ingest", "home-hub-ecg-01", now, benchPlain); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecryptPayload(b *testing.B) {
	bundle, keys := newBenchBundle(b)
	env, err := EncryptPayload(bundle, "/ingest", "home-hub-ecg-01", time.Now(), benchPlain)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecryptPayload(keys, "/ingest", env); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRoundTrip(b *testing.B) {
	bundle, keys := newBenchBundle(b)
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, err := EncryptPayload(bundle, "/ingest", "home-hub-ecg-01", now, benchPlain)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := DecryptPayload(keys, "/ingest", env); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNewKeyMaterial(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewKeyMaterial("k1"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReportEnvelopeSizes(t *testing.T) {
	km, err := NewKeyMaterial("k1")
	if err != nil {
		t.Fatal(err)
	}
	env, err := EncryptPayload(km.Bundle(), "/ingest", "home-hub-ecg-01", time.Now(), benchPlain)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(env)
	bundleJSON, _ := json.Marshal(km.Bundle())
	t.Logf("SIZE plaintext_payload_bytes=%d", len(benchPlain))
	t.Logf("SIZE encrypted_envelope_wire_bytes=%d", len(wire))
	t.Logf("SIZE envelope_overhead_bytes=%d", len(wire)-len(benchPlain))
	t.Logf("SIZE server_key_bundle_bytes=%d", len(bundleJSON))
}
