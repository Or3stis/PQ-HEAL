package server

import (
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
	"time"

	"pq-heal/internal/secure"
)

func decodeRequestPayload(w http.ResponseWriter, r *http.Request, maxBodyBytes int64, payload any, endpoint string, keyManager *KeyManager) (string, error) {
	raw, err := readBodyLimited(w, r, maxBodyBytes)
	if err != nil {
		return "", err
	}
	if !secure.IsEnvelopeJSON(raw) {
		return "", secure.DecodeStrictJSON(raw, payload)
	}

	var env secure.EncryptedEnvelope
	if err := secure.DecodeStrictJSON(raw, &env); err != nil {
		return "", err
	}

	plaintext, err := secure.DecryptPayload(keyManager.KeySetSnapshot(), endpoint, env)
	if err != nil {
		return "", err
	}
	if err := secure.DecodeStrictJSON(plaintext, payload); err != nil {
		return "", err
	}
	if err := validateEnvelopeBinding(env, payload); err != nil {
		return "", err
	}
	return secure.ReplayCacheKey(env.ClientID, env.Nonce, env.Timestamp), nil
}

func readBodyLimited(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func validateEnvelopeBinding(env secure.EncryptedEnvelope, payload any) error {
	var id string
	var ts *time.Time
	switch p := payload.(type) {
	case *HeartbeatPayload:
		id, ts = p.ID, p.Timestamp
	case *IngestPayload:
		id, ts = p.ID, p.Timestamp
	default:
		return nil
	}
	if strings.TrimSpace(id) != strings.TrimSpace(env.ClientID) {
		return secure.ErrInvalidEnvelope
	}
	if ts == nil || !ts.UTC().Equal(env.Timestamp.UTC()) {
		return secure.ErrInvalidEnvelope
	}
	return nil
}

func authorizeRequest(r *http.Request, apiKey string) bool {
	const prefix = "Bearer "
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	token := strings.TrimPrefix(auth, prefix)
	apiKeyBytes := []byte(apiKey)
	tokenBytes := []byte(token)
	if len(tokenBytes) != len(apiKeyBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(tokenBytes, apiKeyBytes) == 1
}
