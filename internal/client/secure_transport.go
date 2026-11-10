package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"pq-heal/internal/secure"
	"strings"
	"sync"
	"time"
)

type SecurePoster struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	keyPin     string
	bundleTTL  time.Duration // 0 disables TTL-based refresh

	mu        sync.RWMutex
	bundle    secure.ServerKeyBundle
	fetchedAt time.Time
	have      bool
}

var (
	ErrMissingKeyPin  = errors.New("missing key pin for encrypted mode")
	ErrKeyPinMismatch = errors.New("server key pin mismatch")
)

// NewSecurePoster constructs a SecurePoster. bundleTTL bounds how long the
// cached /keys bundle is reused before being refetched. A zero or negative
// value disables TTL-based refresh (the bundle still refreshes when the server
// reports unknown_key_id or decryption_failed).
func NewSecurePoster(httpClient *http.Client, baseURL, apiKey, keyPin string, bundleTTL time.Duration) *SecurePoster {
	return &SecurePoster{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     apiKey,
		keyPin:     strings.TrimSpace(keyPin),
		bundleTTL:  bundleTTL,
	}
}

func (p *SecurePoster) Post(endpoint string, payload any, clientID string, at time.Time) error {
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if err := p.attempt(endpoint, plaintext, clientID, at); err != nil {
		if shouldRefreshOnError(err) {
			p.invalidate()
			return p.attempt(endpoint, plaintext, clientID, at)
		}
		return err
	}
	return nil
}

func (p *SecurePoster) attempt(endpoint string, plaintext []byte, clientID string, at time.Time) error {
	bundle, err := p.getBundle(at)
	if err != nil {
		return err
	}
	env, err := secure.EncryptPayload(bundle, endpoint, clientID, at.UTC(), plaintext)
	if err != nil {
		return err
	}
	return PostJSON(p.httpClient, p.baseURL+endpoint, env, p.apiKey)
}

// shouldRefreshOnError returns true when the server response indicates the
// cached bundle is stale relative to the server's current key set.
func shouldRefreshOnError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "unknown_key_id" || apiErr.Code == "decryption_failed"
}

func (p *SecurePoster) invalidate() {
	p.mu.Lock()
	p.have = false
	p.bundle = secure.ServerKeyBundle{}
	p.fetchedAt = time.Time{}
	p.mu.Unlock()
}

func (p *SecurePoster) getBundle(at time.Time) (secure.ServerKeyBundle, error) {
	p.mu.RLock()
	cached, have := p.bundle, p.have
	fresh := have && !p.expired(at)
	p.mu.RUnlock()

	if fresh {
		return cached, nil
	}

	bundle, err := FetchServerKeyBundle(p.httpClient, p.baseURL, p.keyPin)
	if err != nil {
		// A TTL-triggered refresh that fails transiently should not drop a still
		// valid cached bundle — fall back to it. After invalidate() (have=false,
		// e.g. server reported a stale key), there is nothing cached, so the
		// error propagates and the send fails as it should.
		if have {
			return cached, nil
		}
		return secure.ServerKeyBundle{}, err
	}

	p.mu.Lock()
	p.bundle = bundle
	p.fetchedAt = at
	p.have = true
	p.mu.Unlock()
	return bundle, nil
}

// expired reports whether the cached bundle is past its TTL relative to `at`.
// Must be called with p.mu held (RLock is sufficient).
func (p *SecurePoster) expired(at time.Time) bool {
	if p.bundleTTL <= 0 || p.fetchedAt.IsZero() {
		return false
	}
	return at.Sub(p.fetchedAt) > p.bundleTTL
}

func FetchServerKeyBundle(httpClient *http.Client, baseURL string, expectedPin string) (secure.ServerKeyBundle, error) {
	if strings.TrimSpace(expectedPin) == "" {
		return secure.ServerKeyBundle{}, ErrMissingKeyPin
	}

	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/keys"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return secure.ServerKeyBundle{}, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return secure.ServerKeyBundle{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg := readLimited(resp.Body, 512)
		return secure.ServerKeyBundle{}, fmt.Errorf("key endpoint %s: %s", resp.Status, msg)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return secure.ServerKeyBundle{}, err
	}

	var bundle secure.ServerKeyBundle
	if err := secure.DecodeStrictJSON(body, &bundle); err != nil {
		return secure.ServerKeyBundle{}, err
	}
	if strings.TrimSpace(bundle.KeyID) == "" || strings.TrimSpace(bundle.X25519Public) == "" || strings.TrimSpace(bundle.KEMPublic) == "" {
		return secure.ServerKeyBundle{}, fmt.Errorf("incomplete key bundle")
	}
	if !secure.VerifyKeyBundleFingerprint(bundle, expectedPin) {
		return secure.ServerKeyBundle{}, ErrKeyPinMismatch
	}
	return bundle, nil
}
