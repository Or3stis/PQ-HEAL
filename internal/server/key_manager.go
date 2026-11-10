package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"pq-heal/internal/secure"
)

const (
	persistedKeyRingVersion = 1
	// maxRetainedKeys bounds how many keys the manager keeps: the current key
	// plus up to maxRetainedKeys-1 previous keys for in-flight/clock-skew
	// tolerance. Older keys are pruned on rotation and on load so a long-ago
	// key cannot decrypt fresh traffic and the keyring cannot grow unbounded.
	maxRetainedKeys = 3
)

type KeySetResponse struct {
	CurrentKeyID string                   `json:"current_key_id"`
	Keys         []secure.ServerKeyBundle `json:"keys"`
}

type KeyManager struct {
	mu      sync.RWMutex
	path    string
	current string
	keys    map[string]*secure.KeyMaterial
}

func NewKeyManager(path string) (*KeyManager, error) {
	manager := &KeyManager{
		path: strings.TrimSpace(path),
		keys: make(map[string]*secure.KeyMaterial),
	}
	if manager.path == "" {
		km, err := secure.NewKeyMaterial("v1")
		if err != nil {
			return nil, err
		}
		manager.current = km.KeyID
		manager.keys[km.KeyID] = km
		return manager, nil
	}

	if err := manager.loadOrCreate(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *KeyManager) CurrentBundle() secure.ServerKeyBundle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if km, ok := m.keys[m.current]; ok && km != nil {
		return km.Bundle()
	}
	return secure.ServerKeyBundle{}
}

func (m *KeyManager) KeySetSnapshot() map[string]*secure.KeyMaterial {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*secure.KeyMaterial, len(m.keys))
	maps.Copy(out, m.keys)
	return out
}

func (m *KeyManager) KeySetResponse() KeySetResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]secure.ServerKeyBundle, 0, len(m.keys))
	if current, ok := m.keys[m.current]; ok && current != nil {
		keys = append(keys, current.Bundle())
	}

	ids := make([]string, 0, len(m.keys))
	for id := range m.keys {
		if id != m.current {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		km := m.keys[id]
		if km == nil {
			continue
		}
		keys = append(keys, km.Bundle())
	}

	return KeySetResponse{
		CurrentKeyID: m.current,
		Keys:         keys,
	}
}

func (m *KeyManager) Rotate() (secure.ServerKeyBundle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	nextID := nextVersionedKeyID(m.current)
	for {
		if _, exists := m.keys[nextID]; !exists {
			break
		}
		nextID = nextVersionedKeyID(nextID)
	}

	km, err := secure.NewKeyMaterial(nextID)
	if err != nil {
		return secure.ServerKeyBundle{}, err
	}
	prevCurrent := m.current
	m.keys[km.KeyID] = km
	m.current = km.KeyID
	pruned := m.pruneLocked()

	if m.path != "" {
		if err := m.saveLocked(); err != nil {
			delete(m.keys, km.KeyID)
			m.current = prevCurrent
			maps.Copy(m.keys, pruned)
			return secure.ServerKeyBundle{}, err
		}
	}
	return km.Bundle(), nil
}

// pruneLocked drops the oldest keys (by version) until at most maxRetainedKeys
// remain, always keeping the current key. It returns the removed key material so
// callers can restore it if a subsequent persistence step fails. Must be called
// with m.mu held.
func (m *KeyManager) pruneLocked() map[string]*secure.KeyMaterial {
	removed := make(map[string]*secure.KeyMaterial)
	if len(m.keys) <= maxRetainedKeys {
		return removed
	}

	others := make([]string, 0, len(m.keys))
	for id := range m.keys {
		if id != m.current {
			others = append(others, id)
		}
	}
	// Highest version first so the freshest previous keys survive.
	sort.Slice(others, func(i, j int) bool {
		return keyVersion(others[i]) > keyVersion(others[j])
	})

	for _, id := range others[min(len(others), maxRetainedKeys-1):] {
		removed[id] = m.keys[id]
		delete(m.keys, id)
	}
	return removed
}

func (m *KeyManager) loadOrCreate() error {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		km, genErr := secure.NewKeyMaterial("v1")
		if genErr != nil {
			return genErr
		}
		m.current = km.KeyID
		m.keys = map[string]*secure.KeyMaterial{km.KeyID: km}
		return m.saveLocked()
	}

	var ring secure.PersistedKeyRing
	if err := secure.DecodeStrictJSON(raw, &ring); err != nil {
		return fmt.Errorf("decode keyring %q: %w", m.path, err)
	}
	if ring.Version != persistedKeyRingVersion {
		return fmt.Errorf("unsupported keyring version %d", ring.Version)
	}
	if strings.TrimSpace(ring.CurrentKeyID) == "" {
		return errors.New("missing current_key_id")
	}
	if len(ring.Keys) == 0 {
		return errors.New("empty keys")
	}

	loaded := make(map[string]*secure.KeyMaterial, len(ring.Keys))
	for _, rec := range ring.Keys {
		km, err := secure.KeyMaterialFromPersisted(rec)
		if err != nil {
			return fmt.Errorf("decode persisted key %q: %w", rec.KeyID, err)
		}
		loaded[km.KeyID] = km
	}

	current := strings.TrimSpace(ring.CurrentKeyID)
	if _, ok := loaded[current]; !ok {
		return fmt.Errorf("current key_id %q not present", current)
	}

	m.keys = loaded
	m.current = current
	// Enforce retention on an oversized persisted ring; the pruned set is
	// rewritten to disk on the next rotation.
	m.pruneLocked()
	return nil
}

func (m *KeyManager) saveLocked() error {
	if m.path == "" {
		return nil
	}

	ids := make([]string, 0, len(m.keys))
	for id := range m.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]secure.PersistedKey, 0, len(ids))
	for _, id := range ids {
		rec, err := m.keys[id].Persisted()
		if err != nil {
			return err
		}
		records = append(records, rec)
	}

	ring := secure.PersistedKeyRing{
		Version:      persistedKeyRingVersion,
		CurrentKeyID: m.current,
		Keys:         records,
	}
	body, err := json.MarshalIndent(ring, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// keyVersion extracts the numeric suffix of a "v<N>" key id, or -1 if the id
// is not in that form (such keys sort oldest and are pruned first).
func keyVersion(id string) int {
	if after, ok := strings.CutPrefix(strings.TrimSpace(id), "v"); ok {
		if n, err := strconv.Atoi(after); err == nil {
			return n
		}
	}
	return -1
}

func nextVersionedKeyID(current string) string {
	cur := strings.TrimSpace(current)
	if after, ok := strings.CutPrefix(cur, "v"); ok {
		if n, err := strconv.Atoi(after); err == nil && n >= 0 {
			return fmt.Sprintf("v%d", n+1)
		}
	}
	return "v1"
}
