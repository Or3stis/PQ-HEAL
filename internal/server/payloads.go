package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pq-heal/internal/protocol"
)

type IngestPayload struct {
	SubjectID string            `json:"subject_id,omitempty"`
	ID        string            `json:"id"`
	Type      protocol.DataType `json:"type"`
	Value     json.RawMessage   `json:"value"`
	Unit      string            `json:"unit,omitempty"`
	Timestamp *time.Time        `json:"timestamp,omitempty"`
}

type HeartbeatPayload = protocol.HeartbeatPayload

const (
	// MaxReplayAge is the oldest accepted payload timestamp.
	MaxReplayAge = 2 * time.Minute
	// MaxFutureSkew is the allowed client clock skew into the future.
	MaxFutureSkew = 15 * time.Second
	// MaxRequestBodyBytes caps decoded request bodies on ingest/heartbeat.
	MaxRequestBodyBytes = 32 << 10
	// ReplayCacheSlack extends the replay-cache TTL beyond MaxReplayAge+MaxFutureSkew
	// so an envelope cannot be replayed at the very edge of timestamp validity.
	ReplayCacheSlack = 30 * time.Second
)

var (
	ErrIDRequired           = errors.New("id is required")
	ErrUnsupportedType      = errors.New("unsupported type")
	ErrStatusInvalid        = errors.New("status must be 'alive' when provided")
	ErrValueRequired        = errors.New("value is required")
	ErrValueMustBeNumber    = errors.New("value must be a number")
	ErrMotionValueInvalid   = errors.New("motion value must be boolean or number")
	ErrTimestampRequired    = errors.New("timestamp is required")
	ErrTimestampTooOld      = errors.New("timestamp is too old")
	ErrTimestampTooFarAhead = errors.New("timestamp is too far in the future")
)

// Reading is a validated ingest payload.
//
// Display is the form written to the CSV log and shown on the dashboard;
// Numeric is the same reading at full precision. Both are kept because the
// detector must score the reading as sent: re-parsing the rounded display form
// would blunt the model against the corpus it was trained on and make a sensor
// drifting below the second decimal place look stuck.
type Reading struct {
	SubjectID string
	ID        string
	Type      protocol.DataType
	Display   string
	Numeric   float64 // motion is carried as 1 (moved) or 0 (still)
	Unit      string
	DataAt    time.Time
}

// parseValue converts raw JSON into the server's display string format and the
// numeric value behind it.
func parseValue(dataType protocol.DataType, raw json.RawMessage) (string, float64, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", 0, ErrValueRequired
	}

	switch dataType {
	case protocol.DataMotion:
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			if b {
				return "motion", 1, nil
			}
			return "no motion", 0, nil
		}

		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			if f != 0 {
				return "motion", 1, nil
			}
			return "no motion", 0, nil
		}

		return "", 0, ErrMotionValueInvalid
	case protocol.DataHeartRate, protocol.DataPersonTemp, protocol.DataAmbientTemp:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return "", 0, ErrValueMustBeNumber
		}
		return fmt.Sprintf("%.2f", f), f, nil
	default:
		return "", 0, ErrUnsupportedType
	}
}

// ValidateIngestPayload normalizes fields and fills defaults (unit, timestamp).
func ValidateIngestPayload(p IngestPayload) (Reading, error) {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return Reading{}, ErrIDRequired
	}
	if !protocol.IsValidType(p.Type) {
		return Reading{}, ErrUnsupportedType
	}

	display, numeric, err := parseValue(p.Type, p.Value)
	if err != nil {
		return Reading{}, err
	}

	unit := protocol.UnitForType(p.Type)
	if unit == "" {
		return Reading{}, ErrUnsupportedType
	}
	at, err := validateReplayTimestamp(p.Timestamp)
	if err != nil {
		return Reading{}, err
	}

	return Reading{
		SubjectID: strings.TrimSpace(p.SubjectID),
		ID:        id,
		Type:      p.Type,
		Display:   display,
		Numeric:   numeric,
		Unit:      unit,
		DataAt:    at,
	}, nil
}

// ValidateHeartbeatPayload validates identity/type and normalizes timestamps.
func ValidateHeartbeatPayload(p HeartbeatPayload) (string, protocol.DataType, time.Time, error) {
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", "", time.Time{}, ErrIDRequired
	}
	if !protocol.IsValidType(p.Type) {
		return "", "", time.Time{}, ErrUnsupportedType
	}
	if p.Status != "" && p.Status != "alive" {
		return "", "", time.Time{}, ErrStatusInvalid
	}

	at, err := validateReplayTimestamp(p.Timestamp)
	if err != nil {
		return "", "", time.Time{}, err
	}

	return id, p.Type, at, nil
}

// plaintextReplayKey derives a replay-cache key for an unencrypted request.
// An encrypted request keys on its envelope's random nonce; a plaintext body
// carries no nonce, so identity, endpoint and the client's own timestamp are
// what make a resend recognisable. Clients stamp each send with time.Now() at
// nanosecond precision, so this cannot collide for a well-behaved client, and
// the "plain" prefix keeps the two key spaces disjoint.
func plaintextReplayKey(endpoint string, id string, dataType protocol.DataType, at time.Time) string {
	return "plain|" + endpoint + "|" + id + "|" + string(dataType) + "|" + at.UTC().Format(time.RFC3339Nano)
}

func validateReplayTimestamp(ts *time.Time) (time.Time, error) {
	if ts == nil {
		return time.Time{}, ErrTimestampRequired
	}

	now := time.Now().UTC()
	at := ts.UTC()
	if at.After(now.Add(MaxFutureSkew)) {
		return time.Time{}, ErrTimestampTooFarAhead
	}
	if now.Sub(at) > MaxReplayAge {
		return time.Time{}, ErrTimestampTooOld
	}
	return at, nil
}
