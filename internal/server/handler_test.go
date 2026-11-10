package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pq-heal/internal/protocol"
	"pq-heal/internal/secure"
)

func TestHeartbeatHandler(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/heartbeat", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "method_not_allowed")
	})

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "unauthorized")
	})

	t.Run("accepted", func(t *testing.T) {
		ts := time.Now().UTC().Add(-1 * time.Second)
		payload := HeartbeatPayload{
			ID:        "client-1",
			Type:      protocol.DataHeartRate,
			Status:    "alive",
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", rec.Code)
		}
		assertSuccessResponse(t, rec, "alive")
	})

	t.Run("unauthorized echoes provided request id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewBufferString(`{}`))
		req.Header.Set("X-Request-ID", "test-request-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "unauthorized")
		if got := rec.Header().Get("X-Request-ID"); got != "test-request-id" {
			t.Fatalf("expected echoed request id, got %q", got)
		}
	})

	t.Run("conflict on type mismatch", func(t *testing.T) {
		_ = store.UpsertHeartbeat("client-2", protocol.DataHeartRate, time.Now().UTC())
		ts := time.Now().UTC().Add(-1 * time.Second)
		payload := HeartbeatPayload{
			ID:        "client-2",
			Type:      protocol.DataMotion,
			Status:    "alive",
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "client_type_mismatch")
	})

	t.Run("bad request on trailing JSON content", func(t *testing.T) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		body := []byte(`{"id":"client-5","type":"heart_rate","status":"alive","timestamp":"` + now + `"}{"extra":true}`)
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "trailing_json_content")
	})

	t.Run("bad request on old timestamp", func(t *testing.T) {
		old := time.Now().UTC().Add(-1 * (MaxReplayAge + time.Second))
		payload := HeartbeatPayload{
			ID:        "client-old",
			Type:      protocol.DataHeartRate,
			Status:    "alive",
			Timestamp: &old,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "timestamp_too_old")
	})
}

func TestIngestHandler(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "method_not_allowed")
	})

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "unauthorized")
	})

	t.Run("accepted", func(t *testing.T) {
		ts := time.Now().UTC().Add(-1 * time.Second)
		payload := IngestPayload{
			ID:        "client-3",
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
			t.Fatalf("expected 202, got %d", rec.Code)
		}
		assertSuccessResponse(t, rec, "accepted")
	})

	t.Run("bad request on invalid payload", func(t *testing.T) {
		body := []byte(`{"type":"heart_rate","value":10}`)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("conflict on type mismatch", func(t *testing.T) {
		_ = store.UpsertData("client-4", protocol.DataHeartRate, "70.00", "BPM", time.Now().UTC(), time.Now().UTC())
		ts := time.Now().UTC().Add(-1 * time.Second)
		payload := IngestPayload{
			ID:        "client-4",
			Type:      protocol.DataMotion,
			Value:     json.RawMessage("true"),
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "client_type_mismatch")
	})

	t.Run("evicts oldest when at capacity", func(t *testing.T) {
		limitedStore := NewStore(1)
		_ = limitedStore.UpsertHeartbeat("client-old", protocol.DataHeartRate, time.Now().UTC().Add(-time.Minute))
		limitedHandler := newTestHandler(t, HandlerConfig{Store: limitedStore, APIKey: "secret"})
		ts := time.Now().UTC().Add(-1 * time.Second)
		payload := IngestPayload{
			ID:        "client-new",
			Type:      protocol.DataAmbientTemp,
			Value:     json.RawMessage("21.5"),
			Timestamp: &ts,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		limitedHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 (oldest evicted), got %d", rec.Code)
		}
		snapshot := limitedStore.Snapshot()
		if len(snapshot) != 1 || snapshot[0].ID != "client-new" {
			t.Fatalf("expected store to contain only client-new, got %v", snapshot)
		}
	})

	t.Run("bad request on trailing JSON content", func(t *testing.T) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		body := []byte(`{"id":"client-6","type":"ambient_temperature","value":21.5,"timestamp":"` + now + `"}{"extra":true}`)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("bad request on missing timestamp", func(t *testing.T) {
		body := []byte(`{"id":"client-7","type":"ambient_temperature","value":21.5}`)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "timestamp_required")
	})

	t.Run("bad request on unsupported type", func(t *testing.T) {
		ts := time.Now().UTC()
		body := []byte(`{"id":"client-8","type":"unknown","value":21.5,"timestamp":"` + ts.Format(time.RFC3339Nano) + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "unsupported_type")
	})

	t.Run("bad request on invalid json", func(t *testing.T) {
		body := []byte(`{"id":"client-9",`)
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "invalid_json")
	})
}

func TestHealthHandler(t *testing.T) {
	handler := newTestHandler(t, HandlerConfig{Store: NewStore(DefaultMaxClients), APIKey: "secret"})

	t.Run("ok without auth", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if got := rec.Body.String(); got != "ok" {
			t.Fatalf("body = %q, want %q", got, "ok")
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodGet {
			t.Errorf("Allow = %q, want %q", got, http.MethodGet)
		}
		assertErrorCode(t, rec, "method_not_allowed")
	})
}

func TestClientsHandler(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/clients", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
		assertErrorCode(t, rec, "method_not_allowed")
	})

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/clients", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("empty store returns empty array", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/clients", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var views []clientView
		if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(views) != 0 {
			t.Fatalf("expected empty array, got %d items", len(views))
		}
	})

	t.Run("returns client after heartbeat", func(t *testing.T) {
		_ = store.UpsertHeartbeat("hb-client", protocol.DataHeartRate, time.Now().UTC())
		req := httptest.NewRequest(http.MethodGet, "/clients", nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var views []clientView
		if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		var found bool
		for _, v := range views {
			if v.ID == "hb-client" {
				found = true
				if v.Type != string(protocol.DataHeartRate) {
					t.Fatalf("expected type heart_rate, got %q", v.Type)
				}
				if v.HasValue {
					t.Fatalf("expected HasValue=false for heartbeat-only client")
				}
			}
		}
		if !found {
			t.Fatalf("hb-client not found in /clients response")
		}
	})
}

func TestEncryptedIngestAndHeartbeat(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})

	keys := fetchKeys(t, handler)

	t.Run("encrypted heartbeat accepted", func(t *testing.T) {
		now := time.Now().UTC()
		payload := HeartbeatPayload{
			ID:        "enc-client-1",
			Type:      protocol.DataHeartRate,
			Status:    "alive",
			Timestamp: &now,
		}
		req := encryptedRequest(t, keys, "/heartbeat", payload, payload.ID, now)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", rec.Code)
		}
		assertSuccessResponse(t, rec, "alive")
	})

	t.Run("encrypted ingest accepted", func(t *testing.T) {
		now := time.Now().UTC()
		payload := IngestPayload{
			ID:        "enc-client-2",
			Type:      protocol.DataHeartRate,
			Value:     json.RawMessage("68.1"),
			Timestamp: &now,
		}
		req := encryptedRequest(t, keys, "/ingest", payload, payload.ID, now)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", rec.Code)
		}
		assertSuccessResponse(t, rec, "accepted")
	})
}

func TestEncryptedReplayRejected(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})
	keys := fetchKeys(t, handler)

	now := time.Now().UTC()
	payload := HeartbeatPayload{
		ID:        "enc-client-replay",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &now,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := secure.EncryptPayload(keys, "/heartbeat", payload.ID, now, body)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	envBody, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(envBody))
	req1.Header.Set("Authorization", "Bearer secret")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on first request, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(envBody))
	req2.Header.Set("Authorization", "Bearer secret")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for replay, got %d", rec2.Code)
	}
	assertErrorCode(t, rec2, "replay_detected")
}

// Plaintext bodies carry no envelope nonce, so the replay key is derived from
// the client's own identity and timestamp. Resending a captured body must be
// rejected the same way a replayed envelope is.
func TestPlaintextReplayRejected(t *testing.T) {
	handler := newTestHandler(t, HandlerConfig{Store: NewStore(DefaultMaxClients), APIKey: "secret"})

	ts := time.Now().UTC().Add(-time.Second)
	body, err := json.Marshal(IngestPayload{
		ID:        "plain-client",
		Type:      protocol.DataHeartRate,
		Value:     json.RawMessage("72"),
		Timestamp: &ts,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := post(); rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on first request, got %d", rec.Code)
	}
	rec := post()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for replayed plaintext body, got %d", rec.Code)
	}
	assertErrorCode(t, rec, "replay_detected")
}

// A fresh reading from the same client must still be accepted: the key includes
// the timestamp, so only an identical resend is a replay.
func TestPlaintextDistinctTimestampsAccepted(t *testing.T) {
	handler := newTestHandler(t, HandlerConfig{Store: NewStore(DefaultMaxClients), APIKey: "secret"})

	for i := range 3 {
		ts := time.Now().UTC().Add(-time.Duration(i) * time.Second)
		body, err := json.Marshal(IngestPayload{
			ID:        "plain-client-2",
			Type:      protocol.DataHeartRate,
			Value:     json.RawMessage("72"),
			Timestamp: &ts,
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("reading %d: expected 202, got %d", i, rec.Code)
		}
	}
}

func TestInvalidEncryptedEnvelopeDoesNotPoisonReplayCache(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})
	keys := fetchKeys(t, handler)

	now := time.Now().UTC()
	payload := HeartbeatPayload{
		ID:        "enc-client-retry",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &now,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := secure.EncryptPayload(keys, "/heartbeat", payload.ID, now, body)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	envBody, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var tampered map[string]any
	if err := json.Unmarshal(envBody, &tampered); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	tampered["ciphertext"] = "AAAA"
	tamperedBody, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("marshal tampered envelope: %v", err)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(tamperedBody))
	badReq.Header.Set("Authorization", "Bearer secret")
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for tampered envelope, got %d", badRec.Code)
	}
	assertErrorCode(t, badRec, "decryption_failed")

	goodReq := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(envBody))
	goodReq.Header.Set("Authorization", "Bearer secret")
	goodRec := httptest.NewRecorder()
	handler.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for valid retry, got %d", goodRec.Code)
	}
	assertSuccessResponse(t, goodRec, "alive")
}

func TestInvalidTimestampEncryptedEnvelopeDoesNotPoisonReplayCache(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret"})
	keys := fetchKeys(t, handler)

	old := time.Now().UTC().Add(-1 * (MaxReplayAge + time.Second))
	payload := HeartbeatPayload{
		ID:        "enc-client-old-ts",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &old,
	}
	req := encryptedRequest(t, keys, "/heartbeat", payload, payload.ID, old)
	req.Header.Set("Authorization", "Bearer secret")

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on first old timestamp request, got %d", rec1.Code)
	}
	assertErrorCode(t, rec1, "timestamp_too_old")

	req2 := encryptedRequest(t, keys, "/heartbeat", payload, payload.ID, old)
	req2.Header.Set("Authorization", "Bearer secret")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on second old timestamp request, got %d", rec2.Code)
	}
	assertErrorCode(t, rec2, "timestamp_too_old")
}

func TestKeyRotationAndPersistence(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	keyStore := filepath.Join(t.TempDir(), "server-keys.json")
	handler := newTestHandler(t, HandlerConfig{Store: store, APIKey: "secret", KeyStorePath: keyStore})

	oldKeys := fetchKeys(t, handler)

	rotateReq := httptest.NewRequest(http.MethodPost, "/keys/rotate", nil)
	rotateReq.Header.Set("Authorization", "Bearer secret")
	rotateRec := httptest.NewRecorder()
	handler.ServeHTTP(rotateRec, rotateReq)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /keys/rotate, got %d", rotateRec.Code)
	}
	var rotated secure.ServerKeyBundle
	if err := json.NewDecoder(rotateRec.Body).Decode(&rotated); err != nil {
		t.Fatalf("decode rotated bundle: %v", err)
	}
	if rotated.KeyID == oldKeys.KeyID {
		t.Fatalf("expected rotated key id to differ from old key id %q", oldKeys.KeyID)
	}

	allReq := httptest.NewRequest(http.MethodGet, "/keys/all", nil)
	allReq.Header.Set("Authorization", "Bearer secret")
	allRec := httptest.NewRecorder()
	handler.ServeHTTP(allRec, allReq)
	if allRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /keys/all, got %d", allRec.Code)
	}

	// Without auth, /keys/all must be rejected (it exposes rotation history).
	unauthReq := httptest.NewRequest(http.MethodGet, "/keys/all", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from unauthenticated /keys/all, got %d", unauthRec.Code)
	}
	var all KeySetResponse
	if err := json.NewDecoder(allRec.Body).Decode(&all); err != nil {
		t.Fatalf("decode key set: %v", err)
	}
	if all.CurrentKeyID != rotated.KeyID {
		t.Fatalf("expected current key id %q, got %q", rotated.KeyID, all.CurrentKeyID)
	}
	if len(all.Keys) != 2 {
		t.Fatalf("expected 2 active keys after rotation, got %d", len(all.Keys))
	}

	now := time.Now().UTC()
	oldPayload := HeartbeatPayload{
		ID:        "enc-client-old-key",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &now,
	}
	oldReq := encryptedRequest(t, oldKeys, "/heartbeat", oldPayload, oldPayload.ID, now)
	oldReq.Header.Set("Authorization", "Bearer secret")
	oldRec := httptest.NewRecorder()
	handler.ServeHTTP(oldRec, oldReq)
	if oldRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with old key, got %d", oldRec.Code)
	}

	newPayload := HeartbeatPayload{
		ID:        "enc-client-new-key",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &now,
	}
	newReq := encryptedRequest(t, rotated, "/heartbeat", newPayload, newPayload.ID, now)
	newReq.Header.Set("Authorization", "Bearer secret")
	newRec := httptest.NewRecorder()
	handler.ServeHTTP(newRec, newReq)
	if newRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with new key, got %d", newRec.Code)
	}

	reloaded := newTestHandler(t, HandlerConfig{Store: NewStore(DefaultMaxClients), APIKey: "secret", KeyStorePath: keyStore})

	reloadedAt := now.Add(time.Second)
	reloadedPayload := HeartbeatPayload{
		ID:        oldPayload.ID,
		Type:      oldPayload.Type,
		Status:    oldPayload.Status,
		Timestamp: &reloadedAt,
	}
	reloadReq := encryptedRequest(t, oldKeys, "/heartbeat", reloadedPayload, reloadedPayload.ID, reloadedAt)
	reloadReq.Header.Set("Authorization", "Bearer secret")
	reloadRec := httptest.NewRecorder()
	reloaded.ServeHTTP(reloadRec, reloadReq)
	if reloadRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on reloaded server with old key, got %d", reloadRec.Code)
	}
}

func newTestHandler(t *testing.T, cfg HandlerConfig) http.Handler {
	t.Helper()
	h, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func fetchKeys(t *testing.T, handler http.Handler) secure.ServerKeyBundle {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/keys", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /keys, got %d", rec.Code)
	}
	var bundle secure.ServerKeyBundle
	if err := json.NewDecoder(rec.Body).Decode(&bundle); err != nil {
		t.Fatalf("decode keys: %v", err)
	}
	return bundle
}

func encryptedRequest(t *testing.T, keys secure.ServerKeyBundle, endpoint string, payload any, clientID string, at time.Time) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := secure.EncryptPayload(keys, endpoint, clientID, at, body)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	envBody, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(envBody))
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var response apiErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("expected JSON error response, decode failed: %v", err)
	}
	if response.Error.Code != want {
		t.Fatalf("expected error code %q, got %q", want, response.Error.Code)
	}
	if response.Error.Message == "" {
		t.Fatalf("expected non-empty error message")
	}
	if response.Error.RequestID == "" {
		t.Fatalf("expected non-empty request_id in response body")
	}
	headerRequestID := rec.Header().Get("X-Request-ID")
	if headerRequestID == "" {
		t.Fatalf("expected non-empty X-Request-ID header")
	}
	if headerRequestID != response.Error.RequestID {
		t.Fatalf("expected header and body request_id to match, got %q and %q", headerRequestID, response.Error.RequestID)
	}
}

func assertSuccessResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus string) {
	t.Helper()
	var response apiSuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("expected JSON success response, decode failed: %v", err)
	}
	if response.Status != wantStatus {
		t.Fatalf("expected status %q, got %q", wantStatus, response.Status)
	}
	if response.RequestID == "" {
		t.Fatalf("expected non-empty request_id in response body")
	}
	headerRequestID := rec.Header().Get("X-Request-ID")
	if headerRequestID == "" {
		t.Fatalf("expected non-empty X-Request-ID header")
	}
	if headerRequestID != response.RequestID {
		t.Fatalf("expected header and body request_id to match, got %q and %q", headerRequestID, response.RequestID)
	}
}
