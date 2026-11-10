package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

func TestIngestRateLimitedPerClient(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{
		Store:          store,
		APIKey:         "secret",
		RateLimitRPS:   0.001, // effectively no refill within the test
		RateLimitBurst: 2,
	})

	send := func(id string) *httptest.ResponseRecorder {
		ts := time.Now().UTC().Add(-time.Second)
		payload := IngestPayload{
			ID:        id,
			Type:      protocol.DataAmbientTemp,
			Value:     json.RawMessage("21.5"),
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for i := range 2 {
		if rec := send("client-rl"); rec.Code != http.StatusAccepted {
			t.Fatalf("burst token %d: expected 202, got %d", i, rec.Code)
		}
	}
	rec := send("client-rl")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst exhausted, got %d", rec.Code)
	}
	assertErrorCode(t, rec, "rate_limited")

	// Other clients have independent buckets.
	if rec := send("client-other"); rec.Code != http.StatusAccepted {
		t.Fatalf("independent client should still be allowed, got %d", rec.Code)
	}
}

func TestRateLimitDisabledByDefault(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})

	for i := range 20 {
		ts := time.Now().UTC().Add(-time.Second)
		payload := IngestPayload{
			ID:        "client-unlimited",
			Type:      protocol.DataAmbientTemp,
			Value:     json.RawMessage("21.5"),
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("unlimited request %d expected 202, got %d", i, rec.Code)
		}
	}
}
