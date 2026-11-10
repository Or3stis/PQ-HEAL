package server

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

func TestParseValue(t *testing.T) {
	display, numeric, err := parseValue(protocol.DataHeartRate, json.RawMessage("72"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if display != "72.00" || numeric != 72 {
		t.Fatalf("expected 72.00/72, got %q/%v", display, numeric)
	}

	// The display form rounds to two decimals; the numeric form must not, or
	// the detector scores a different reading than the client sent.
	display, numeric, err = parseValue(protocol.DataPersonTemp, json.RawMessage("36.456"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if display != "36.46" {
		t.Fatalf("display = %q, want 36.46", display)
	}
	if numeric != 36.456 {
		t.Fatalf("numeric = %v, want 36.456 (full precision)", numeric)
	}

	display, numeric, err = parseValue(protocol.DataMotion, json.RawMessage("true"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if display != "motion" || numeric != 1 {
		t.Fatalf("expected motion/1, got %q/%v", display, numeric)
	}

	_, _, err = parseValue(protocol.DataMotion, json.RawMessage("\"bad\""))
	if err == nil {
		t.Fatalf("expected error for invalid motion value")
	}
}

func TestValidateIngestPayload(t *testing.T) {
	when := time.Now().UTC().Add(-1 * time.Second)
	payload := IngestPayload{
		ID:        "client-1",
		Type:      protocol.DataAmbientTemp,
		Value:     json.RawMessage("21.5"),
		Unit:      "F",
		Timestamp: &when,
	}

	reading, err := ValidateIngestPayload(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reading.ID != "client-1" || reading.Type != protocol.DataAmbientTemp ||
		reading.Display != "21.50" || reading.Numeric != 21.5 || reading.Unit != "C" {
		t.Fatalf("unexpected payload values: %+v", reading)
	}
	if !reading.DataAt.Equal(when) {
		t.Fatalf("expected timestamp to be preserved")
	}
}

func TestValidateHeartbeatPayload(t *testing.T) {
	when := time.Now().UTC().Add(-1 * time.Second)
	payload := HeartbeatPayload{
		ID:        "client-2",
		Type:      protocol.DataMotion,
		Status:    "alive",
		Timestamp: &when,
	}

	id, dataType, at, err := ValidateHeartbeatPayload(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "client-2" || dataType != protocol.DataMotion {
		t.Fatalf("unexpected heartbeat values")
	}
	if !at.Equal(when) {
		t.Fatalf("expected timestamp to be preserved")
	}
}

func TestValidatePayloadTimestampReplayProtection(t *testing.T) {
	now := time.Now().UTC()

	ingestMissing := IngestPayload{
		ID:    "client-1",
		Type:  protocol.DataHeartRate,
		Value: json.RawMessage("72"),
	}
	if _, err := ValidateIngestPayload(ingestMissing); err == nil {
		t.Fatalf("expected missing timestamp error")
	}

	old := now.Add(-1 * (MaxReplayAge + time.Second))
	ingestOld := IngestPayload{
		ID:        "client-1",
		Type:      protocol.DataHeartRate,
		Value:     json.RawMessage("72"),
		Timestamp: &old,
	}
	if _, err := ValidateIngestPayload(ingestOld); err == nil {
		t.Fatalf("expected old timestamp to be rejected")
	}

	future := now.Add(MaxFutureSkew + time.Second)
	heartbeatFuture := HeartbeatPayload{
		ID:        "client-2",
		Type:      protocol.DataMotion,
		Status:    "alive",
		Timestamp: &future,
	}
	if _, _, _, err := ValidateHeartbeatPayload(heartbeatFuture); err == nil {
		t.Fatalf("expected future timestamp to be rejected")
	}
}

func TestStoreUpsertTypeMismatch(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	now := time.Now().UTC()

	err := store.UpsertHeartbeat("client-1", protocol.DataHeartRate, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = store.UpsertData("client-1", protocol.DataMotion, "motion", "", now, now)
	if err == nil {
		t.Fatalf("expected type mismatch error")
	}
}

func TestStoreSnapshotSorting(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	now := time.Now().UTC()

	_ = store.UpsertHeartbeat("b", protocol.DataHeartRate, now)
	_ = store.UpsertHeartbeat("a", protocol.DataHeartRate, now)
	_ = store.UpsertHeartbeat("c", protocol.DataMotion, now)

	snapshot := store.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("expected 3 items")
	}
	if snapshot[0].ID != "a" || snapshot[1].ID != "b" || snapshot[2].ID != "c" {
		t.Fatalf("unexpected sort order: %v %v %v", snapshot[0].ID, snapshot[1].ID, snapshot[2].ID)
	}
}

func TestStoreEvictsOldestWhenAtCapacity(t *testing.T) {
	store := NewStore(2)
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertHeartbeat("oldest", protocol.DataHeartRate, base); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.UpsertHeartbeat("newer", protocol.DataHeartRate, base.Add(time.Minute)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Insert a third client over capacity; the one with the smallest LastSeen
	// ("oldest") should be evicted to make room.
	if err := store.UpsertHeartbeat("newest", protocol.DataHeartRate, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("expected eviction, got error: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 clients after eviction, got %d", len(snapshot))
	}
	for _, rec := range snapshot {
		if rec.ID == "oldest" {
			t.Fatalf("expected 'oldest' to be evicted, snapshot=%v", snapshot)
		}
	}
}

func TestStoreEvictionPreservesTypeMismatch(t *testing.T) {
	// Eviction must not bypass the type-consistency invariant: re-registering
	// an existing client under a different type still errors.
	store := NewStore(2)
	now := time.Now().UTC()
	if err := store.UpsertHeartbeat("client-1", protocol.DataHeartRate, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := store.UpsertHeartbeat("client-1", protocol.DataAmbientTemp, now)
	if !errors.Is(err, ErrClientTypeMismatch) {
		t.Fatalf("expected ErrClientTypeMismatch, got %v", err)
	}
}

func TestStoreUpsertDataCorrectness(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	now := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)

	err := store.UpsertData("client-1", protocol.DataHeartRate, "72.00", "BPM", now, now.Add(time.Second))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected 1 item, got %d", len(snapshot))
	}
	rec := snapshot[0]
	if rec.ID != "client-1" {
		t.Fatalf("expected id to be set")
	}
	if rec.Type != protocol.DataHeartRate {
		t.Fatalf("expected type to be set")
	}
	if rec.Value != "72.00" || rec.Unit != "BPM" {
		t.Fatalf("unexpected value/unit: %q %q", rec.Value, rec.Unit)
	}
	if !rec.DataAt.Equal(now) {
		t.Fatalf("expected data timestamp to be set")
	}
	if !rec.LastSeen.Equal(now.Add(time.Second)) {
		t.Fatalf("expected last seen to be set")
	}
	if !rec.HasValue {
		t.Fatalf("expected HasValue to be true")
	}
}

func TestStoreUpsertDataIgnoresOlderTimestamp(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	base := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertData("client-1", protocol.DataHeartRate, "80.00", "BPM", base, base.Add(2*time.Second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	older := base.Add(-1 * time.Minute)
	if err := store.UpsertData("client-1", protocol.DataHeartRate, "60.00", "BPM", older, base.Add(4*time.Second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected 1 item, got %d", len(snapshot))
	}
	rec := snapshot[0]
	if rec.Value != "80.00" {
		t.Fatalf("expected latest value to remain 80.00, got %q", rec.Value)
	}
	if !rec.DataAt.Equal(base) {
		t.Fatalf("expected data timestamp %v, got %v", base, rec.DataAt)
	}
	if !rec.LastSeen.Equal(base.Add(4 * time.Second)) {
		t.Fatalf("expected last seen to be refreshed by stale event")
	}
}

// TestValidatePayloadTimestampBoundaries checks that timestamps near (but safely
// within) each boundary are accepted, and timestamps just beyond are rejected.
// Note: exact nanosecond boundary testing is not deterministic because
// validateReplayTimestamp calls time.Now() internally.
func TestValidatePayloadTimestampBoundaries(t *testing.T) {
	// Near-old boundary: MaxReplayAge - 1s → should be accepted.
	nearOld := time.Now().UTC().Add(-(MaxReplayAge - time.Second))
	ingest := IngestPayload{
		ID:        "client-boundary",
		Type:      protocol.DataHeartRate,
		Value:     json.RawMessage("72"),
		Timestamp: &nearOld,
	}
	if _, err := ValidateIngestPayload(ingest); err != nil {
		t.Fatalf("expected timestamp near MaxReplayAge to be accepted, got %v", err)
	}

	// Just over old boundary: MaxReplayAge + 1s → should be rejected.
	overOld := time.Now().UTC().Add(-(MaxReplayAge + time.Second))
	ingest.Timestamp = &overOld
	if _, err := ValidateIngestPayload(ingest); !errors.Is(err, ErrTimestampTooOld) {
		t.Fatalf("expected ErrTimestampTooOld for timestamp beyond MaxReplayAge, got %v", err)
	}

	// Near-future boundary: MaxFutureSkew - 1s → should be accepted.
	nearFuture := time.Now().UTC().Add(MaxFutureSkew - time.Second)
	hb := HeartbeatPayload{
		ID:        "client-boundary",
		Type:      protocol.DataHeartRate,
		Status:    "alive",
		Timestamp: &nearFuture,
	}
	if _, _, _, err := ValidateHeartbeatPayload(hb); err != nil {
		t.Fatalf("expected timestamp near MaxFutureSkew to be accepted, got %v", err)
	}

	// Just over future boundary: MaxFutureSkew + 1s → should be rejected.
	overFuture := time.Now().UTC().Add(MaxFutureSkew + time.Second)
	hb.Timestamp = &overFuture
	if _, _, _, err := ValidateHeartbeatPayload(hb); !errors.Is(err, ErrTimestampTooFarAhead) {
		t.Fatalf("expected ErrTimestampTooFarAhead for timestamp beyond MaxFutureSkew, got %v", err)
	}
}
