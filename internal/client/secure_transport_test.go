package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"pq-heal/internal/secure"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchServerKeyBundleRequiresPin(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("request should not be sent when pin is missing")
			return nil, nil
		}),
	}

	_, err := FetchServerKeyBundle(httpClient, "http://example", "")
	if err != ErrMissingKeyPin {
		t.Fatalf("expected ErrMissingKeyPin, got %v", err)
	}
}

func TestFetchServerKeyBundleRejectsPinMismatch(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	body := marshalBundle(t, km.Bundle())
	httpClient := newStubClient(http.StatusOK, body)

	_, err = FetchServerKeyBundle(httpClient, "http://example", strings.Repeat("0", 64))
	if err != ErrKeyPinMismatch {
		t.Fatalf("expected ErrKeyPinMismatch, got %v", err)
	}
}

func TestFetchServerKeyBundleAcceptsValidPin(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	bundle := km.Bundle()
	body := marshalBundle(t, bundle)
	httpClient := newStubClient(http.StatusOK, body)

	got, err := FetchServerKeyBundle(httpClient, "http://example", secure.KeyBundleFingerprint(bundle))
	if err != nil {
		t.Fatalf("FetchServerKeyBundle() error = %v", err)
	}
	if got.KeyID != bundle.KeyID {
		t.Fatalf("expected key id %q, got %q", bundle.KeyID, got.KeyID)
	}
}

func newStubClient(statusCode int, body []byte) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: statusCode,
				Status:     http.StatusText(statusCode),
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
}

func marshalBundle(t *testing.T, bundle secure.ServerKeyBundle) []byte {
	t.Helper()
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return data
}

func TestFetchServerKeyBundleRejectsIncompleteBundle(t *testing.T) {
	// Completeness check fires before pin check, so any non-empty pin works here.
	const anyPin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64 chars

	cases := []struct {
		name   string
		bundle secure.ServerKeyBundle
	}{
		{"empty KeyID", secure.ServerKeyBundle{X25519Public: "x", KEMPublic: "y", KEMAlgorithm: "ML-KEM-768"}},
		{"empty X25519Public", secure.ServerKeyBundle{KeyID: "v1", KEMPublic: "y", KEMAlgorithm: "ML-KEM-768"}},
		{"empty KEMPublic", secure.ServerKeyBundle{KeyID: "v1", X25519Public: "x", KEMAlgorithm: "ML-KEM-768"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := marshalBundle(t, tc.bundle)
			httpClient := newStubClient(http.StatusOK, body)
			_, err := FetchServerKeyBundle(httpClient, "http://example", anyPin)
			if err == nil {
				t.Fatal("expected error for incomplete bundle, got nil")
			}
		})
	}
}

func TestSecurePosterRefreshesAfterTTL(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	bundle := km.Bundle()
	pin := secure.KeyBundleFingerprint(bundle)

	keyFetches := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/keys") {
			keyFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(marshalBundle(t, bundle))),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"accepted"}`))),
			Header:     make(http.Header),
		}, nil
	})

	const ttl = 100 * time.Millisecond
	poster := NewSecurePoster(&http.Client{Transport: transport}, "http://example", "", pin, ttl)

	t0 := time.Now().UTC()
	_ = poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", t0)
	// Second call within TTL: cache hit.
	_ = poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", t0.Add(ttl/2))
	if keyFetches != 1 {
		t.Fatalf("expected 1 key fetch within TTL, got %d", keyFetches)
	}
	// Third call after TTL: cache should refresh.
	_ = poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", t0.Add(2*ttl))
	if keyFetches != 2 {
		t.Fatalf("expected 2 key fetches after TTL expiry, got %d", keyFetches)
	}
}

func TestSecurePosterFallsBackToCachedBundleOnRefreshFailure(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	bundle := km.Bundle()
	pin := secure.KeyBundleFingerprint(bundle)

	keyFetches := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/keys") {
			keyFetches++
			// First fetch succeeds; any later (TTL-triggered) refetch fails.
			if keyFetches > 1 {
				return nil, fmt.Errorf("simulated network failure")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(marshalBundle(t, bundle))),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"accepted"}`))),
			Header:     make(http.Header),
		}, nil
	})

	const ttl = 100 * time.Millisecond
	poster := NewSecurePoster(&http.Client{Transport: transport}, "http://example", "", pin, ttl)

	t0 := time.Now().UTC()
	if err := poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", t0); err != nil {
		t.Fatalf("initial Post failed: %v", err)
	}
	// After TTL the refetch fails, but the cached bundle is still valid, so the
	// send must succeed rather than error out.
	if err := poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", t0.Add(2*ttl)); err != nil {
		t.Fatalf("Post after failed refresh should fall back to cached bundle, got %v", err)
	}
	if keyFetches != 2 {
		t.Fatalf("expected 2 key fetch attempts (initial + failed refresh), got %d", keyFetches)
	}
}

func TestSecurePosterRefreshesOnUnknownKeyID(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	bundle := km.Bundle()
	pin := secure.KeyBundleFingerprint(bundle)

	keyFetches := 0
	postCalls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/keys") {
			keyFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(marshalBundle(t, bundle))),
				Header:     make(http.Header),
			}, nil
		}
		postCalls++
		if postCalls == 1 {
			// First post: server rejects with unknown_key_id.
			body := []byte(`{"error":{"code":"unknown_key_id","message":"unknown key id","request_id":"abc"}}`)
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}
		// Retry succeeds.
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"accepted"}`))),
			Header:     make(http.Header),
		}, nil
	})

	poster := NewSecurePoster(&http.Client{Transport: transport}, "http://example", "", pin, time.Hour)

	if err := poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", time.Now().UTC()); err != nil {
		t.Fatalf("Post after refresh-and-retry expected success, got %v", err)
	}
	if keyFetches != 2 {
		t.Fatalf("expected 2 key fetches (initial + after unknown_key_id), got %d", keyFetches)
	}
	if postCalls != 2 {
		t.Fatalf("expected 2 post attempts (original + retry), got %d", postCalls)
	}
}

func TestSecurePosterCachesBundle(t *testing.T) {
	km, err := secure.NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	bundle := km.Bundle()
	pin := secure.KeyBundleFingerprint(bundle)

	keyFetches := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/keys") {
			keyFetches++
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(marshalBundle(t, bundle))),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"accepted"}`))),
			Header:     make(http.Header),
		}, nil
	})

	poster := NewSecurePoster(&http.Client{Transport: transport}, "http://example", "", pin, time.Hour)
	now := time.Now().UTC()

	// Two calls to Post — errors from PostJSON are irrelevant; we only count key fetches.
	_ = poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", now)
	_ = poster.Post("/heartbeat", map[string]string{"status": "alive"}, "c1", now.Add(time.Second))

	if keyFetches != 1 {
		t.Fatalf("expected exactly 1 key fetch (cache hit on 2nd call), got %d", keyFetches)
	}
}
