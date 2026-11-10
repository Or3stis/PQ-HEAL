package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pq-heal/internal/secure"
)

// rotateResponse extends the server key bundle with its SHA-256 fingerprint
// so operators can roll out new -key-pin values without local computation.
type rotateResponse struct {
	secure.ServerKeyBundle
	Fingerprint string `json:"fingerprint"`
}

type clientView struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Value    string     `json:"value,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	DataAt   *time.Time `json:"data_at,omitempty"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	HasValue bool       `json:"has_value"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type apiErrorResponse struct {
	Error apiError `json:"error"`
}

type apiSuccessResponse struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
}

var validationErrorCodes = []struct {
	err  error
	code string
}{
	{ErrIDRequired, "id_required"},
	{ErrUnsupportedType, "unsupported_type"},
	{ErrStatusInvalid, "invalid_status"},
	{ErrValueRequired, "value_required"},
	{ErrValueMustBeNumber, "value_not_number"},
	{ErrMotionValueInvalid, "motion_value_invalid"},
	{ErrTimestampRequired, "timestamp_required"},
	{ErrTimestampTooOld, "timestamp_too_old"},
	{ErrTimestampTooFarAhead, "timestamp_in_future"},
	{secure.ErrTrailingJSONContent, "trailing_json_content"},
	{secure.ErrUnknownKeyID, "unknown_key_id"},
	{secure.ErrInvalidEnvelope, "invalid_encrypted_envelope"},
	{secure.ErrDecryptionFailed, "decryption_failed"},
	{secure.ErrReplayDetected, "replay_detected"},
	{secure.ErrUnsupportedKEMAlg, "unsupported_kem_algorithm"},
}

func clientRecordsToViews(records []ClientRecord) []clientView {
	views := make([]clientView, len(records))
	for i, rec := range records {
		v := clientView{ID: rec.ID, Type: string(rec.Type), HasValue: rec.HasValue}
		if rec.HasValue {
			v.Value = rec.Value
			v.Unit = rec.Unit
			t := rec.DataAt
			v.DataAt = &t
		}
		if !rec.LastSeen.IsZero() {
			t := rec.LastSeen
			v.LastSeen = &t
		}
		views[i] = v
	}
	return views
}

func writeValidationError(w http.ResponseWriter, r *http.Request, err error) {
	code := validationErrorCode(err)

	message := err.Error()
	if code == "invalid_json" {
		message = "invalid JSON payload"
	}

	writeAPIError(w, r, http.StatusBadRequest, code, message)
}

func validationErrorCode(err error) string {
	for _, e := range validationErrorCodes {
		if errors.Is(err, e.err) {
			return e.code
		}
	}
	return "invalid_json"
}

func writeRateLimitError(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, r, http.StatusTooManyRequests, "rate_limited", "per-client request rate exceeded")
}

func writeConflictError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrClientTypeMismatch):
		writeAPIError(w, r, http.StatusConflict, "client_type_mismatch", err.Error())
	default:
		writeAPIError(w, r, http.StatusConflict, "conflict", err.Error())
	}
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code string, message string) {
	requestID := requestIDFromHeaderOrRequest(w, r)
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorResponse{
		Error: apiError{
			Code:      code,
			Message:   message,
			RequestID: requestID,
		},
	})
}

func writeSuccessResponse(w http.ResponseWriter, r *http.Request, statusCode int, status string) {
	requestID := requestIDFromHeaderOrRequest(w, r)
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(apiSuccessResponse{
		Status:    status,
		RequestID: requestID,
	})
}

// maxClientRequestIDLen caps the length of a client-supplied X-Request-ID
// echoed in responses and logs. Larger values are dropped and a fresh ID is
// generated, preventing log spam and header bloat.
const maxClientRequestIDLen = 64

func requestIDForRequest(r *http.Request) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Request-ID")); v != "" && isValidRequestID(v) {
			return v
		}
	}
	const n = 12
	var b [n]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("fallback-%d", time.Now().UTC().UnixNano())
}

// isValidRequestID restricts client-supplied request IDs to safe characters
// (letters, digits, '-', '_') so they can be echoed and logged without
// injecting control characters into log lines.
func isValidRequestID(v string) bool {
	if len(v) == 0 || len(v) > maxClientRequestIDLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

func requestIDFromHeaderOrRequest(w http.ResponseWriter, r *http.Request) string {
	if v := strings.TrimSpace(w.Header().Get("X-Request-ID")); v != "" {
		return v
	}
	return requestIDForRequest(r)
}
