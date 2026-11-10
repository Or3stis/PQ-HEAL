package secure

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewKeyMaterialAndBundle(t *testing.T) {
	km, err := NewKeyMaterial(" v1 ")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}

	bundle := km.Bundle()
	if bundle.KeyID != "v1" {
		t.Fatalf("expected trimmed key_id, got %q", bundle.KeyID)
	}
	if bundle.KEMAlgorithm != "ML-KEM-768" {
		t.Fatalf("expected ML-KEM-768, got %q", bundle.KEMAlgorithm)
	}
	if _, err := base64.RawStdEncoding.DecodeString(bundle.X25519Public); err != nil {
		t.Fatalf("x25519 public key is not valid base64: %v", err)
	}
	if _, err := base64.RawStdEncoding.DecodeString(bundle.KEMPublic); err != nil {
		t.Fatalf("kem public key is not valid base64: %v", err)
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	at := time.Now().UTC().Truncate(time.Second)
	plaintext := []byte(`{"id":"client-1","type":"heart_rate","value":72.3}`)

	env, err := EncryptPayload(bundle, "/ingest", "client-1", at, plaintext)
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}

	got, err := DecryptPayload(keys, "/ingest", env)
	if err != nil {
		t.Fatalf("DecryptPayload() error = %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip plaintext mismatch: got=%q want=%q", got, plaintext)
	}
}

func TestEncryptPayloadUnsupportedKEMAlgorithm(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	bundle := km.Bundle()
	bundle.KEMAlgorithm = "X25519-PLACEHOLDER"

	_, err = EncryptPayload(bundle, "/ingest", "client-1", time.Now().UTC(), []byte(`{}`))
	if !errors.Is(err, ErrUnsupportedKEMAlg) {
		t.Fatalf("expected ErrUnsupportedKEMAlg, got %v", err)
	}
}

func TestDecryptPayloadUnknownKeyID(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	bundle := km.Bundle()
	env, err := EncryptPayload(bundle, "/heartbeat", "client-1", time.Now().UTC(), []byte(`{"status":"alive"}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}

	_, err = DecryptPayload(map[string]*KeyMaterial{}, "/heartbeat", env)
	if !errors.Is(err, ErrUnknownKeyID) {
		t.Fatalf("expected ErrUnknownKeyID, got %v", err)
	}
}

func TestDecryptPayloadTamperedCiphertext(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	env, err := EncryptPayload(bundle, "/heartbeat", "client-1", time.Now().UTC(), []byte(`{"status":"alive"}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}

	rawCipher, err := base64.RawStdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	rawCipher[len(rawCipher)-1] ^= 0x01
	env.Ciphertext = base64.RawStdEncoding.EncodeToString(rawCipher)

	_, err = DecryptPayload(keys, "/heartbeat", env)
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestDecryptPayloadMalformedEnvelope(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	env, err := EncryptPayload(bundle, "/ingest", "client-1", time.Now().UTC(), []byte(`{"value":1}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}
	env.Nonce = "not-base64!"

	_, err = DecryptPayload(keys, "/ingest", env)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("expected ErrInvalidEnvelope, got %v", err)
	}
}

func TestIsEnvelopeJSON(t *testing.T) {
	body := []byte(`{"key_id":"v1","client_id":"c1","timestamp":"2026-02-16T00:00:00Z","client_x25519_pub":"x","kem_ciphertext":"y","nonce":"n","ciphertext":"c"}`)
	if !IsEnvelopeJSON(body) {
		t.Fatal("expected envelope JSON to be detected")
	}

	notEnvelope := []byte(`{"id":"client-1","type":"heart_rate","value":70}`)
	if IsEnvelopeJSON(notEnvelope) {
		t.Fatal("expected non-envelope JSON to be rejected")
	}
}

func TestDecodeStrictJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	var okPayload payload
	if err := DecodeStrictJSON([]byte(`{"name":"pq-heal"}`), &okPayload); err != nil {
		t.Fatalf("DecodeStrictJSON valid payload error = %v", err)
	}
	if okPayload.Name != "pq-heal" {
		t.Fatalf("unexpected decoded value: %q", okPayload.Name)
	}

	var unknown payload
	if err := DecodeStrictJSON([]byte(`{"name":"pq-heal","extra":1}`), &unknown); err == nil {
		t.Fatal("expected unknown field error")
	}

	var trailing payload
	err := DecodeStrictJSON([]byte(`{"name":"pq-heal"}{"x":1}`), &trailing)
	if !errors.Is(err, ErrTrailingJSONContent) {
		t.Fatalf("expected ErrTrailingJSONContent for trailing object, got %v", err)
	}
}

func TestReplayCacheKey(t *testing.T) {
	at := time.Date(2026, 2, 16, 12, 0, 1, 987654321, time.FixedZone("EET", 2*3600))
	key := ReplayCacheKey(" client-1 ", " nonce-1 ", at)
	want := "client-1|nonce-1|2026-02-16T10:00:01.987654321Z"
	if key != want {
		t.Fatalf("ReplayCacheKey() = %q, want %q", key, want)
	}
}

func TestEnvelopeCanBeJSONRoundTripped(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	bundle := km.Bundle()

	env, err := EncryptPayload(bundle, "/ingest", "client-1", time.Now().UTC(), []byte(`{"value":21.1}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}

	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if !IsEnvelopeJSON(body) {
		t.Fatal("expected marshaled encrypted envelope to be detected")
	}
}

func TestKeyBundleFingerprintAndVerify(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	bundle := km.Bundle()
	pin := KeyBundleFingerprint(bundle)
	if len(pin) != 64 {
		t.Fatalf("expected hex sha256 pin length 64, got %d", len(pin))
	}
	if !VerifyKeyBundleFingerprint(bundle, strings.ToUpper(pin)) {
		t.Fatal("expected pin verification to be case-insensitive")
	}

	other, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(KeyBundleFingerprint(other.Bundle()))) == 1 {
		t.Fatal("expected different bundles to produce different pins")
	}
}

func TestPersistedKeyRoundTrip(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}

	rec, err := km.Persisted()
	if err != nil {
		t.Fatalf("Persisted() error = %v", err)
	}
	if rec.KEMAlgorithm != "ML-KEM-768" {
		t.Fatalf("unexpected kem algorithm: %q", rec.KEMAlgorithm)
	}

	loaded, err := KeyMaterialFromPersisted(rec)
	if err != nil {
		t.Fatalf("KeyMaterialFromPersisted() error = %v", err)
	}
	if loaded.KeyID != "v1" {
		t.Fatalf("expected key id v1, got %q", loaded.KeyID)
	}

	keys := map[string]*KeyMaterial{"v1": loaded}
	env, err := EncryptPayload(loaded.Bundle(), "/heartbeat", "client-1", time.Now().UTC(), []byte(`{"status":"alive"}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}
	if _, err := DecryptPayload(keys, "/heartbeat", env); err != nil {
		t.Fatalf("DecryptPayload() error = %v", err)
	}
}

func TestDecryptPayloadInvalidBase64Fields(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	makeEnv := func() EncryptedEnvelope {
		t.Helper()
		env, err := EncryptPayload(bundle, "/heartbeat", "client-1", time.Now().UTC(), []byte(`{"status":"alive"}`))
		if err != nil {
			t.Fatalf("EncryptPayload() error = %v", err)
		}
		return env
	}

	t.Run("invalid base64 in ClientX25519Pub", func(t *testing.T) {
		env := makeEnv()
		env.ClientX25519Pub = "not-base64!"
		if _, err := DecryptPayload(keys, "/heartbeat", env); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("expected ErrInvalidEnvelope, got %v", err)
		}
	})

	t.Run("invalid base64 in KEMCiphertext", func(t *testing.T) {
		env := makeEnv()
		env.KEMCiphertext = "not-base64!"
		if _, err := DecryptPayload(keys, "/heartbeat", env); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("expected ErrInvalidEnvelope, got %v", err)
		}
	})

	t.Run("invalid base64 in Ciphertext", func(t *testing.T) {
		env := makeEnv()
		env.Ciphertext = "not-base64!"
		if _, err := DecryptPayload(keys, "/heartbeat", env); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("expected ErrInvalidEnvelope, got %v", err)
		}
	})
}

func TestDecryptPayloadWrongNonceSize(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	env, err := EncryptPayload(bundle, "/ingest", "client-1", time.Now().UTC(), []byte(`{"value":1}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}
	// 3-byte nonce is valid base64 but wrong length for AES-GCM (needs 12 bytes).
	env.Nonce = b64([]byte{1, 2, 3})

	if _, err := DecryptPayload(keys, "/ingest", env); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("expected ErrInvalidEnvelope for wrong nonce size, got %v", err)
	}
}

func TestDecryptPayloadTruncatedCiphertext(t *testing.T) {
	km, err := NewKeyMaterial("v1")
	if err != nil {
		t.Fatalf("NewKeyMaterial() error = %v", err)
	}
	keys := map[string]*KeyMaterial{"v1": km}
	bundle := km.Bundle()

	env, err := EncryptPayload(bundle, "/ingest", "client-1", time.Now().UTC(), []byte(`{"value":42}`))
	if err != nil {
		t.Fatalf("EncryptPayload() error = %v", err)
	}
	// Replace ciphertext with a valid-base64 but too-short value (4 bytes, not a valid GCM ciphertext).
	env.Ciphertext = b64([]byte{1, 2, 3, 4})

	if _, err := DecryptPayload(keys, "/ingest", env); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed for truncated ciphertext, got %v", err)
	}
}

func TestIsEnvelopeJSONPartialFields(t *testing.T) {
	// All seven fields present → true (already tested in TestIsEnvelopeJSON, confirmed here for clarity).
	full := []byte(`{"key_id":"v1","client_id":"c","timestamp":"2026-01-01T00:00:00Z","client_x25519_pub":"x","kem_ciphertext":"y","nonce":"n","ciphertext":"c"}`)
	if !IsEnvelopeJSON(full) {
		t.Fatal("expected full envelope to be detected")
	}

	// Missing one field → false.
	missing := []byte(`{"key_id":"v1","client_id":"c","timestamp":"2026-01-01T00:00:00Z","client_x25519_pub":"x","kem_ciphertext":"y","nonce":"n"}`)
	if IsEnvelopeJSON(missing) {
		t.Fatal("expected partial envelope (missing ciphertext) to be rejected")
	}
}
