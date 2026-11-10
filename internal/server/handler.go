package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pq-heal/internal/secure"
)

// Defaults for handler background-task lifecycles.
const (
	DefaultSweepInterval   = 30 * time.Second
	DefaultIdleBucketAfter = 5 * time.Minute
)

// HandlerConfig configures the HTTP handler returned by NewHandler.
type HandlerConfig struct {
	Store        *Store
	Logger       *CSVLogger
	ClientPorts  *ClientPortLogger
	APIKey       string
	KeyStorePath string // empty disables on-disk keyring persistence

	// Detection scores accepted telemetry off the request path. When nil the
	// hub ingests and stores normally but raises no anomaly or threshold
	// alerts, which is the configuration used by the protocol-level tests.
	Detection *DetectionPipeline

	// RateLimitRPS / RateLimitBurst configure a per-client (by payload id)
	// token bucket on /ingest and /heartbeat. Zero RPS disables limiting.
	RateLimitRPS   float64
	RateLimitBurst float64

	// Context controls the lifetime of background sweepers (replay cache and
	// rate limiter). When nil, no background sweepers run — useful in tests.
	Context context.Context

	// SweepInterval overrides DefaultSweepInterval. Ignored when Context is nil.
	SweepInterval time.Duration

	// IdleBucketAfter overrides DefaultIdleBucketAfter. Ignored when Context is nil
	// or RateLimitRPS is zero.
	IdleBucketAfter time.Duration

	// AccessLog enables one structured log line per HTTP request.
	AccessLog bool
}

// requireMethod checks the HTTP method and writes a 405 if it doesn't match.
// Returns false when the request should be aborted.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

// requireAuth checks the Bearer token and writes a 401 if missing or wrong.
// Returns false when the request should be aborted. Skipped when apiKey is empty.
func requireAuth(w http.ResponseWriter, r *http.Request, apiKey string) bool {
	if apiKey != "" && !authorizeRequest(r, apiKey) {
		writeAPIError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return false
	}
	return true
}

func NewHandler(cfg HandlerConfig) (http.Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("handler config: Store is required")
	}
	store := cfg.Store
	if cfg.Detection != nil {
		cfg.Detection.bindStore(store)
	}
	logger := cfg.Logger
	apiKey := strings.TrimSpace(cfg.APIKey)
	keyManager, err := NewKeyManager(cfg.KeyStorePath)
	if err != nil {
		return nil, fmt.Errorf("initialize encryption keys: %w", err)
	}
	replayCache := NewReplayCache(MaxReplayAge + MaxFutureSkew + ReplayCacheSlack)
	rateLimiter := NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst)

	if cfg.Context != nil {
		interval := cfg.SweepInterval
		if interval <= 0 {
			interval = DefaultSweepInterval
		}
		idleAfter := cfg.IdleBucketAfter
		if idleAfter <= 0 {
			idleAfter = DefaultIdleBucketAfter
		}
		go replayCache.RunSweeper(cfg.Context, interval)
		if cfg.RateLimitRPS > 0 {
			go rateLimiter.RunSweeper(cfg.Context, interval, idleAfter)
		}
	}

	mux := http.NewServeMux()
	registerWebDashboard(mux)

	// Lightweight health endpoint for liveness checks.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Read-only snapshot of all tracked clients.
	mux.HandleFunc("/clients", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodGet) || !requireAuth(w, r, apiKey) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(clientRecordsToViews(store.Snapshot()))
	})

	// Public server key bundle for message-level encryption bootstrap.
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keyManager.CurrentBundle())
	})

	// Full key set (current + previous active keys) for rollout/rotation
	// visibility. This is an operator view (clients only need the current bundle
	// from /keys), so it requires auth to avoid exposing rotation history.
	mux.HandleFunc("/keys/all", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodGet) || !requireAuth(w, r, apiKey) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keyManager.KeySetResponse())
	})

	// Rotate current server key while keeping older keys available for decryption.
	// Response includes the SHA-256 fingerprint so operators can update client
	// -key-pin values without recomputing it themselves.
	mux.HandleFunc("/keys/rotate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodPost) || !requireAuth(w, r, apiKey) {
			return
		}
		bundle, err := keyManager.Rotate()
		if err != nil {
			writeAPIError(w, r, http.StatusInternalServerError, "rotation_failed", "key rotation failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rotateResponse{
			ServerKeyBundle: bundle,
			Fingerprint:     secure.KeyBundleFingerprint(bundle),
		})
	})

	// Heartbeat accepts "alive" pings to refresh LastSeen.
	mux.HandleFunc("/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodPost) || !requireAuth(w, r, apiKey) {
			return
		}

		var payload HeartbeatPayload
		replayKey, err := decodeRequestPayload(w, r, MaxRequestBodyBytes, &payload, "/heartbeat", keyManager)
		if err != nil {
			writeValidationError(w, r, err)
			return
		}

		id, dataType, seenAt, err := ValidateHeartbeatPayload(payload)
		if err != nil {
			writeValidationError(w, r, err)
			return
		}
		if !rateLimiter.Allow(id, time.Now().UTC()) {
			writeRateLimitError(w, r)
			return
		}
		if replayKey == "" {
			replayKey = plaintextReplayKey("/heartbeat", id, dataType, seenAt)
		}
		if replayCache.SeenOrAdd(replayKey, time.Now().UTC()) {
			writeValidationError(w, r, secure.ErrReplayDetected)
			return
		}

		if err := store.UpsertHeartbeat(id, dataType, seenAt); err != nil {
			writeConflictError(w, r, err)
			return
		}

		cfg.ClientPorts.Append(time.Now().UTC(), id, "/heartbeat", r.RemoteAddr, requestIDFromHeaderOrRequest(w, r))
		writeSuccessResponse(w, r, http.StatusAccepted, "alive")
	})

	// Ingest accepts telemetry payloads, validates them, stores, and logs if enabled.
	mux.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestIDForRequest(r))
		if !requireMethod(w, r, http.MethodPost) || !requireAuth(w, r, apiKey) {
			return
		}

		var payload IngestPayload
		replayKey, err := decodeRequestPayload(w, r, MaxRequestBodyBytes, &payload, "/ingest", keyManager)
		if err != nil {
			writeValidationError(w, r, err)
			return
		}

		reading, err := ValidateIngestPayload(payload)
		if err != nil {
			writeValidationError(w, r, err)
			return
		}
		if !rateLimiter.Allow(reading.ID, time.Now().UTC()) {
			writeRateLimitError(w, r)
			return
		}
		if replayKey == "" {
			replayKey = plaintextReplayKey("/ingest", reading.ID, reading.Type, reading.DataAt)
		}
		if replayCache.SeenOrAdd(replayKey, time.Now().UTC()) {
			writeValidationError(w, r, secure.ErrReplayDetected)
			return
		}

		seenAt := time.Now().UTC()
		generation, err := store.upsertData(reading.ID, reading.Type, reading.Display, reading.Unit, reading.DataAt, seenAt)
		if err != nil {
			writeConflictError(w, r, err)
			return
		}

		if logger != nil {
			logger.Append(reading.DataAt, reading.ID, reading.Type, reading.Display, reading.Unit)
		}

		// Detection runs after the reading is durable, so a detector fault can
		// never cost telemetry, and off the request path, so its cost does not
		// enter the client's response time.
		if cfg.Detection != nil {
			// Read the identifier back from the response header rather than
			// regenerating it. requestIDForRequest mints a fresh random value
			// when the client supplies none, so calling it twice would give the
			// alert an identifier that matches neither the response nor the
			// access log, silently breaking alert-to-request correlation.
			cfg.Detection.submit(reading, requestIDFromHeaderOrRequest(w, r), generation)
		}

		cfg.ClientPorts.Append(seenAt, reading.ID, "/ingest", r.RemoteAddr, requestIDFromHeaderOrRequest(w, r))
		writeSuccessResponse(w, r, http.StatusAccepted, "accepted")
	})

	if cfg.AccessLog {
		return withAccessLog(mux), nil
	}
	return mux, nil
}
