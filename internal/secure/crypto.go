package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	circlkem "github.com/cloudflare/circl/kem"
	"github.com/cloudflare/circl/kem/mlkem/mlkem768"
)

const (
	ContextLabel = "pq-heal-envelope-v1"
)

var (
	ErrUnknownKeyID        = errors.New("unknown key id")
	ErrInvalidEnvelope     = errors.New("invalid encrypted envelope")
	ErrDecryptionFailed    = errors.New("encrypted payload decryption failed")
	ErrReplayDetected      = errors.New("encrypted payload replay detected")
	ErrUnsupportedKEMAlg   = errors.New("unsupported kem algorithm")
	ErrTrailingJSONContent = errors.New("trailing content after JSON object")

	mlkemScheme = mlkem768.Scheme()
)

type ServerKeyBundle struct {
	KeyID        string `json:"key_id"`
	X25519Public string `json:"x25519_public"`
	KEMPublic    string `json:"kem_public"`
	KEMAlgorithm string `json:"kem_algorithm"`
}

type EncryptedEnvelope struct {
	KeyID           string    `json:"key_id"`
	ClientID        string    `json:"client_id"`
	Timestamp       time.Time `json:"timestamp"`
	ClientX25519Pub string    `json:"client_x25519_pub"`
	KEMCiphertext   string    `json:"kem_ciphertext"`
	Nonce           string    `json:"nonce"`
	Ciphertext      string    `json:"ciphertext"`
}

type KeyMaterial struct {
	KeyID      string
	x25519Priv *ecdh.PrivateKey
	kemPriv    circlkem.PrivateKey
	kemPubRaw  []byte
}

func NewKeyMaterial(keyID string) (*KeyMaterial, error) {
	curve := ecdh.X25519()
	xPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	kemPub, kemPriv, err := mlkemScheme.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	kemPubRaw, err := kemPub.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return &KeyMaterial{
		KeyID:      strings.TrimSpace(keyID),
		x25519Priv: xPriv,
		kemPriv:    kemPriv,
		kemPubRaw:  kemPubRaw,
	}, nil
}

func (k *KeyMaterial) Bundle() ServerKeyBundle {
	return ServerKeyBundle{
		KeyID:        k.KeyID,
		X25519Public: b64(k.x25519Priv.PublicKey().Bytes()),
		KEMPublic:    b64(k.kemPubRaw),
		KEMAlgorithm: mlkemScheme.Name(),
	}
}

func EncryptPayload(bundle ServerKeyBundle, endpoint, clientID string, at time.Time, plaintext []byte) (EncryptedEnvelope, error) {
	curve := ecdh.X25519()

	xServerPubRaw, err := b64Decode(bundle.X25519Public)
	if err != nil {
		return EncryptedEnvelope{}, fmt.Errorf("decode x25519 server key: %w", ErrInvalidEnvelope)
	}
	xServerPub, err := curve.NewPublicKey(xServerPubRaw)
	if err != nil {
		return EncryptedEnvelope{}, fmt.Errorf("parse x25519 server key: %w", ErrInvalidEnvelope)
	}

	if strings.TrimSpace(bundle.KEMAlgorithm) != mlkemScheme.Name() {
		return EncryptedEnvelope{}, ErrUnsupportedKEMAlg
	}

	kemServerPubRaw, err := b64Decode(bundle.KEMPublic)
	if err != nil {
		return EncryptedEnvelope{}, fmt.Errorf("decode kem server key: %w", ErrInvalidEnvelope)
	}
	kemServerPub, err := mlkemScheme.UnmarshalBinaryPublicKey(kemServerPubRaw)
	if err != nil {
		return EncryptedEnvelope{}, fmt.Errorf("parse kem server key: %w", ErrInvalidEnvelope)
	}

	xClientPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	kemCiphertext, kemShared, err := mlkemScheme.Encapsulate(kemServerPub)
	if err != nil {
		return EncryptedEnvelope{}, err
	}

	ecdhShared, err := xClientPriv.ECDH(xServerPub)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	key, err := deriveAEADKey(ecdhShared, kemShared, bundle.KeyID)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return EncryptedEnvelope{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return EncryptedEnvelope{}, err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return EncryptedEnvelope{}, err
	}

	at = at.UTC()
	aad := buildAAD(endpoint, bundle.KeyID, clientID, at)
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)

	return EncryptedEnvelope{
		KeyID:           bundle.KeyID,
		ClientID:        clientID,
		Timestamp:       at,
		ClientX25519Pub: b64(xClientPriv.PublicKey().Bytes()),
		KEMCiphertext:   b64(kemCiphertext),
		Nonce:           b64(nonce),
		Ciphertext:      b64(ciphertext),
	}, nil
}

func DecryptPayload(keys map[string]*KeyMaterial, endpoint string, env EncryptedEnvelope) ([]byte, error) {
	km, ok := keys[strings.TrimSpace(env.KeyID)]
	if !ok {
		return nil, ErrUnknownKeyID
	}
	curve := ecdh.X25519()

	xClientPubRaw, err := b64Decode(env.ClientX25519Pub)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	xClientPub, err := curve.NewPublicKey(xClientPubRaw)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}

	kemCipherRaw, err := b64Decode(env.KEMCiphertext)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}

	nonce, err := b64Decode(env.Nonce)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	ciphertext, err := b64Decode(env.Ciphertext)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}

	ecdhShared, err := km.x25519Priv.ECDH(xClientPub)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	kemShared, err := mlkemScheme.Decapsulate(km.kemPriv, kemCipherRaw)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	key, err := deriveAEADKey(ecdhShared, kemShared, km.KeyID)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	if len(nonce) != aead.NonceSize() {
		return nil, ErrInvalidEnvelope
	}
	aad := buildAAD(endpoint, km.KeyID, env.ClientID, env.Timestamp.UTC())
	pt, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return pt, nil
}

func IsEnvelopeJSON(body []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	_, hasCiphertext := probe["ciphertext"]
	_, hasNonce := probe["nonce"]
	_, hasKeyID := probe["key_id"]
	_, hasX := probe["client_x25519_pub"]
	_, hasKEM := probe["kem_ciphertext"]
	_, hasClientID := probe["client_id"]
	_, hasTimestamp := probe["timestamp"]
	return hasCiphertext && hasNonce && hasKeyID && hasX && hasKEM && hasClientID && hasTimestamp
}

func DecodeStrictJSON(body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return ErrTrailingJSONContent
	}
	return nil
}

func ReplayCacheKey(clientID, nonce string, at time.Time) string {
	return strings.TrimSpace(clientID) + "|" + strings.TrimSpace(nonce) + "|" + at.UTC().Format(time.RFC3339Nano)
}

// KeyBundleFingerprint returns a stable SHA-256 fingerprint for key pinning.
func KeyBundleFingerprint(bundle ServerKeyBundle) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(bundle.KeyID)))
	h.Write([]byte("|"))
	h.Write([]byte(strings.TrimSpace(bundle.KEMAlgorithm)))
	h.Write([]byte("|"))
	h.Write([]byte(strings.TrimSpace(bundle.X25519Public)))
	h.Write([]byte("|"))
	h.Write([]byte(strings.TrimSpace(bundle.KEMPublic)))
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyKeyBundleFingerprint checks a user-provided pin against the bundle.
func VerifyKeyBundleFingerprint(bundle ServerKeyBundle, expected string) bool {
	got := KeyBundleFingerprint(bundle)
	want := strings.ToLower(strings.TrimSpace(expected))
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func deriveAEADKey(ecdhShared, kemShared []byte, keyID string) ([]byte, error) {
	combined := append(append([]byte{}, ecdhShared...), kemShared...)
	// salt=nil is correct per RFC 5869 §2.2 when no random salt is available.
	// Both the protocol label and key ID belong in info for domain separation.
	info := ContextLabel + "|" + strings.TrimSpace(keyID)
	return hkdf.Key(sha256.New, combined, nil, info, 32)
}

func buildAAD(endpoint, keyID, clientID string, at time.Time) []byte {
	return []byte(strings.TrimSpace(endpoint) + "|" + strings.TrimSpace(keyID) + "|" + strings.TrimSpace(clientID) + "|" + at.UTC().Format(time.RFC3339Nano))
}

func b64(data []byte) string { return base64.RawStdEncoding.EncodeToString(data) }

func b64Decode(s string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimSpace(s))
}
