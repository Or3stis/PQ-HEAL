package secure

import (
	"crypto/ecdh"
	"errors"
	"strings"
)

type PersistedKey struct {
	KeyID        string `json:"key_id"`
	X25519Priv   string `json:"x25519_private"`
	KEMPrivate   string `json:"kem_private"`
	KEMPublic    string `json:"kem_public"`
	KEMAlgorithm string `json:"kem_algorithm"`
}

type PersistedKeyRing struct {
	Version      int            `json:"version"`
	CurrentKeyID string         `json:"current_key_id"`
	Keys         []PersistedKey `json:"keys"`
}

func (k *KeyMaterial) Persisted() (PersistedKey, error) {
	if k == nil || k.x25519Priv == nil || k.kemPriv == nil || len(k.kemPubRaw) == 0 {
		return PersistedKey{}, errors.New("invalid key material")
	}
	kemPrivRaw, err := k.kemPriv.MarshalBinary()
	if err != nil {
		return PersistedKey{}, err
	}
	return PersistedKey{
		KeyID:        strings.TrimSpace(k.KeyID),
		X25519Priv:   b64(k.x25519Priv.Bytes()),
		KEMPrivate:   b64(kemPrivRaw),
		KEMPublic:    b64(k.kemPubRaw),
		KEMAlgorithm: mlkemScheme.Name(),
	}, nil
}

func KeyMaterialFromPersisted(p PersistedKey) (*KeyMaterial, error) {
	keyID := strings.TrimSpace(p.KeyID)
	if keyID == "" {
		return nil, errors.New("missing key_id")
	}
	if strings.TrimSpace(p.KEMAlgorithm) != mlkemScheme.Name() {
		return nil, ErrUnsupportedKEMAlg
	}

	xPrivRaw, err := b64Decode(p.X25519Priv)
	if err != nil {
		return nil, err
	}
	xPriv, err := ecdh.X25519().NewPrivateKey(xPrivRaw)
	if err != nil {
		return nil, err
	}

	kemPrivRaw, err := b64Decode(p.KEMPrivate)
	if err != nil {
		return nil, err
	}
	kemPriv, err := mlkemScheme.UnmarshalBinaryPrivateKey(kemPrivRaw)
	if err != nil {
		return nil, err
	}

	kemPubRaw, err := b64Decode(p.KEMPublic)
	if err != nil {
		return nil, err
	}
	if _, err := mlkemScheme.UnmarshalBinaryPublicKey(kemPubRaw); err != nil {
		return nil, err
	}

	return &KeyMaterial{
		KeyID:      keyID,
		x25519Priv: xPriv,
		kemPriv:    kemPriv,
		kemPubRaw:  kemPubRaw,
	}, nil
}
