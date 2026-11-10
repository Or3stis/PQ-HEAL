package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// --- NewKeyManager ---

func TestNewKeyManagerInMemory(t *testing.T) {
	km, err := NewKeyManager("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if km.current == "" {
		t.Fatal("expected current key to be set")
	}
	if _, ok := km.keys[km.current]; !ok {
		t.Fatal("expected current key to exist in key map")
	}
}

func TestNewKeyManagerCreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	km, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected keyring file to be created: %v", err)
	}
	if km.current == "" {
		t.Fatal("expected current key to be set")
	}
}

func TestNewKeyManagerLoadsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")

	// First call creates the file.
	km1, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("unexpected error creating: %v", err)
	}
	originalID := km1.current

	// Second call should load and return the same key ID.
	km2, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}
	if km2.current != originalID {
		t.Fatalf("expected same key ID %q after reload, got %q", originalID, km2.current)
	}
}

// --- CurrentBundle ---

func TestCurrentBundleIsNonEmpty(t *testing.T) {
	km, _ := NewKeyManager("")
	b := km.CurrentBundle()
	if b.KeyID == "" {
		t.Fatal("expected non-empty KeyID in bundle")
	}
	if b.X25519Public == "" || b.KEMPublic == "" || b.KEMAlgorithm == "" {
		t.Fatal("expected bundle to have all public key fields set")
	}
}

// --- KeySetSnapshot ---

func TestKeySetSnapshotContainsCurrentKey(t *testing.T) {
	km, _ := NewKeyManager("")
	snap := km.KeySetSnapshot()
	if _, ok := snap[km.current]; !ok {
		t.Fatal("expected snapshot to contain current key")
	}
}

func TestKeySetSnapshotIsCopy(t *testing.T) {
	km, _ := NewKeyManager("")
	snap := km.KeySetSnapshot()
	// Mutating the snapshot should not affect the manager.
	delete(snap, km.current)
	snap2 := km.KeySetSnapshot()
	if _, ok := snap2[km.current]; !ok {
		t.Fatal("modifying snapshot should not affect key manager internals")
	}
}

// --- KeySetResponse ---

func TestKeySetResponseCurrentKeyIsFirst(t *testing.T) {
	km, _ := NewKeyManager("")
	resp := km.KeySetResponse()
	if resp.CurrentKeyID != km.current {
		t.Fatalf("expected CurrentKeyID %q, got %q", km.current, resp.CurrentKeyID)
	}
	if len(resp.Keys) == 0 {
		t.Fatal("expected at least one key in response")
	}
	if resp.Keys[0].KeyID != km.current {
		t.Fatalf("expected first key to be current, got %q", resp.Keys[0].KeyID)
	}
}

func TestKeySetResponseAfterRotation(t *testing.T) {
	km, _ := NewKeyManager("")
	oldID := km.current

	if _, err := km.Rotate(); err != nil {
		t.Fatalf("unexpected rotation error: %v", err)
	}

	resp := km.KeySetResponse()
	if resp.CurrentKeyID == oldID {
		t.Fatal("expected current key to change after rotation")
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("expected 2 keys after rotation, got %d", len(resp.Keys))
	}
	// Current key must be first.
	if resp.Keys[0].KeyID != resp.CurrentKeyID {
		t.Fatalf("expected current key first, got %q", resp.Keys[0].KeyID)
	}
}

// --- Rotate ---

func TestRotateChangesCurrentKey(t *testing.T) {
	km, _ := NewKeyManager("")
	before := km.current

	bundle, err := km.Rotate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if km.current == before {
		t.Fatal("expected current key to change after rotation")
	}
	if bundle.KeyID != km.current {
		t.Fatalf("expected returned bundle key ID %q to match new current %q", bundle.KeyID, km.current)
	}
}

func TestRotatePreservesOldKey(t *testing.T) {
	km, _ := NewKeyManager("")
	oldID := km.current

	if _, err := km.Rotate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := km.keys[oldID]; !ok {
		t.Fatal("expected old key to be retained after rotation")
	}
}

func TestRotateMultipleTimes(t *testing.T) {
	km, _ := NewKeyManager("")
	for i := range 3 {
		if _, err := km.Rotate(); err != nil {
			t.Fatalf("rotation %d failed: %v", i+1, err)
		}
	}
	// Retention caps the keyring at maxRetainedKeys regardless of rotations.
	if len(km.keys) != maxRetainedKeys {
		t.Fatalf("expected %d keys after 3 rotations, got %d", maxRetainedKeys, len(km.keys))
	}
}

func TestRotatePrunesOldestKeys(t *testing.T) {
	km, _ := NewKeyManager("")
	oldestID := km.current // "v1"

	for range maxRetainedKeys + 2 {
		if _, err := km.Rotate(); err != nil {
			t.Fatalf("unexpected rotation error: %v", err)
		}
	}

	if len(km.keys) != maxRetainedKeys {
		t.Fatalf("expected keyring capped at %d, got %d", maxRetainedKeys, len(km.keys))
	}
	if _, ok := km.keys[oldestID]; ok {
		t.Fatalf("expected oldest key %q to be pruned", oldestID)
	}
	if _, ok := km.keys[km.current]; !ok {
		t.Fatal("expected current key to be retained after pruning")
	}
}

func TestLoadPrunesOversizedKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	km, _ := NewKeyManager(path)
	// Rotate enough to exceed retention, persisting each step.
	for range maxRetainedKeys + 1 {
		if _, err := km.Rotate(); err != nil {
			t.Fatalf("unexpected rotation error: %v", err)
		}
	}

	km2, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if len(km2.keys) > maxRetainedKeys {
		t.Fatalf("expected loaded keyring capped at %d, got %d", maxRetainedKeys, len(km2.keys))
	}
	if _, ok := km2.keys[km2.current]; !ok {
		t.Fatal("expected current key present after load+prune")
	}
}

func TestRotatePersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	km, _ := NewKeyManager(path)

	if _, err := km.Rotate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify the new current key is present.
	km2, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	if km2.current != km.current {
		t.Fatalf("expected persisted current %q, got %q", km.current, km2.current)
	}
	if len(km2.keys) != len(km.keys) {
		t.Fatalf("expected %d keys after reload, got %d", len(km.keys), len(km2.keys))
	}
}

// --- nextVersionedKeyID ---

func TestNextVersionedKeyID(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"v1", "v2"},
		{"v2", "v3"},
		{"v99", "v100"},
		{"v0", "v1"},
		{"", "v1"},
		{"garbage", "v1"},
		{"v-1", "v1"},
		{"  v3  ", "v4"},
	}
	for _, tc := range cases {
		got := nextVersionedKeyID(tc.input)
		if got != tc.want {
			t.Errorf("nextVersionedKeyID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- loadOrCreate error paths ---

func TestLoadOrCreateRejectsCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewKeyManager(path)
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
}

func TestLoadOrCreateRejectsWrongVersion(t *testing.T) {
	ring := map[string]any{
		"version":        99,
		"current_key_id": "v1",
		"keys":           []any{},
	}
	writeJSON(t, ring)
}

func TestLoadOrCreateRejectsMissingCurrentKeyID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	ring := map[string]any{
		"version":        1,
		"current_key_id": "   ",
		"keys":           []any{map[string]any{"key_id": "v1"}},
	}
	raw, _ := json.Marshal(ring)
	os.WriteFile(path, raw, 0o600)
	_, err := NewKeyManager(path)
	if err == nil {
		t.Fatal("expected error for empty current_key_id")
	}
}

func TestLoadOrCreateRejectsEmptyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	ring := map[string]any{
		"version":        1,
		"current_key_id": "v1",
		"keys":           []any{},
	}
	raw, _ := json.Marshal(ring)
	os.WriteFile(path, raw, 0o600)
	_, err := NewKeyManager(path)
	if err == nil {
		t.Fatal("expected error for empty keys array")
	}
}

func TestLoadOrCreateRejectsCurrentKeyIDNotInKeys(t *testing.T) {
	// Create a valid file first, then corrupt it so current_key_id points
	// to a key that doesn't exist in the keys list.
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	km, err := NewKeyManager(path)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Read the written file and overwrite current_key_id to a non-existent id.
	raw, _ := os.ReadFile(path)
	var ring map[string]any
	json.Unmarshal(raw, &ring)
	ring["current_key_id"] = "does-not-exist"
	_ = km // silence unused warning
	modified, _ := json.Marshal(ring)
	os.WriteFile(path, modified, 0o600)

	_, err = NewKeyManager(path)
	if err == nil {
		t.Fatal("expected error when current_key_id is not present in keys")
	}
}

// --- Concurrent rotate + read ---

func TestKeyManagerConcurrentRotateAndRead(t *testing.T) {
	km, _ := NewKeyManager("")
	var wg sync.WaitGroup

	for range 10 {
		wg.Go(func() {
			km.Rotate() //nolint
		})
	}
	for range 20 {
		wg.Go(func() {
			_ = km.CurrentBundle()
			_ = km.KeySetSnapshot()
			_ = km.KeySetResponse()
		})
	}
	wg.Wait()
}

// writeJSON is a helper used to test wrong-version keyring files.
func writeJSON(t *testing.T, ring map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keyring.json")
	raw, _ := json.Marshal(ring)
	os.WriteFile(path, raw, 0o600)
	_, err := NewKeyManager(path)
	if err == nil {
		t.Fatal("expected error for wrong keyring version")
	}
}
