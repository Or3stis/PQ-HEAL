package server

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"pq-heal/internal/protocol"
)

var (
	ErrClientTypeMismatch = errors.New("client already registered with a different type")
)

// LoadFromCSV reads a historical log file and populates the store with the latest state for each client.
func (s *Store) LoadFromCSV(path string) error {
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1 // allow variable field count so short rows reach the len guard
	// Skip header
	_, err = reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// CSV format: timestamp, id, type, value, unit
		if len(record) < 5 {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, record[0])
		if err != nil {
			continue
		}

		// Undo the anti-formula-injection quoting applied on write, so values
		// such as a negative temperature hydrate as the number they were.
		id := strings.TrimSpace(unsanitizeCSVField(record[1]))
		dataType := protocol.DataType(record[2])
		if id == "" || !protocol.IsValidType(dataType) {
			continue
		}
		val := unsanitizeCSVField(record[3])
		unit := unsanitizeCSVField(record[4])

		// Apply the same type-consistency + capacity invariant as live upserts;
		// a corrupt or oversized log cannot push the store past maxItems or
		// inject unknown types. checkSlot evicts the oldest entry when full.
		if err := s.checkSlot(id, dataType); err != nil {
			continue
		}

		existing, ok := s.items[id]
		if !ok || ts.After(existing.DataAt) {
			s.items[id] = ClientRecord{
				generation: existing.generation,
				ID:         id,
				Type:       dataType,
				Value:      val,
				Unit:       unit,
				DataAt:     ts,
				LastSeen:   ts, // Assume last seen is the same as data timestamp for hydration
				HasValue:   true,
			}
		}
	}

	return nil
}

type ClientRecord struct {
	generation uint64
	ID         string
	Type       protocol.DataType
	Value      string
	Unit       string
	DataAt     time.Time
	LastSeen   time.Time
	HasValue   bool
}

// Store keeps the latest data + heartbeat per client with a size limit.
type Store struct {
	mu               sync.RWMutex
	items            map[string]ClientRecord
	maxItems         int
	nextGeneration   uint64
	evictionRevision uint64
	onEviction       func()
}

const DefaultMaxClients = 1000

func NewStore(maxItems int) *Store {
	if maxItems <= 0 {
		maxItems = DefaultMaxClients
	}
	return &Store{
		items:    make(map[string]ClientRecord),
		maxItems: maxItems,
	}
}

// checkSlot validates type consistency and frees a slot when at capacity by
// evicting the entry with the smallest LastSeen. Must be called with s.mu held.
func (s *Store) checkSlot(id string, dataType protocol.DataType) error {
	if existing, ok := s.items[id]; ok && existing.Type != dataType {
		return ErrClientTypeMismatch
	}
	if _, ok := s.items[id]; ok {
		return nil
	}
	if s.maxItems > 0 && len(s.items) >= s.maxItems {
		s.evictOldestLocked()
	}
	s.nextGeneration++
	s.items[id] = ClientRecord{ID: id, Type: dataType, generation: s.nextGeneration}
	return nil
}

// evictOldestLocked removes the entry with the smallest LastSeen.
// Must be called with s.mu held and only when the map is non-empty.
func (s *Store) evictOldestLocked() {
	var oldestID string
	var oldestSeen time.Time
	first := true
	for id, rec := range s.items {
		if first || rec.LastSeen.Before(oldestSeen) {
			oldestID = id
			oldestSeen = rec.LastSeen
			first = false
		}
	}
	s.removeLocked(oldestID)
}

// UpsertHeartbeat refreshes LastSeen without modifying value/unit.
func (s *Store) UpsertHeartbeat(id string, dataType protocol.DataType, seenAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkSlot(id, dataType); err != nil {
		return err
	}

	rec := s.items[id]
	rec.ID = id
	rec.Type = dataType
	rec.LastSeen = seenAt

	s.items[id] = rec
	return nil
}

// UpsertData stores the latest value and updates LastSeen for the client.
func (s *Store) UpsertData(id string, dataType protocol.DataType, value string, unit string, dataAt time.Time, seenAt time.Time) error {
	_, err := s.upsertData(id, dataType, value, unit, dataAt, seenAt)
	return err
}

// upsertData returns the registration generation atomically with the update.
func (s *Store) upsertData(id string, dataType protocol.DataType, value string, unit string, dataAt time.Time, seenAt time.Time) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkSlot(id, dataType); err != nil {
		return 0, err
	}

	rec := s.items[id]
	rec.ID = id
	rec.Type = dataType
	if rec.HasValue && dataAt.Before(rec.DataAt) {
		rec.LastSeen = seenAt
		s.items[id] = rec
		return rec.generation, nil
	}
	rec.Value = value
	rec.Unit = unit
	rec.DataAt = dataAt
	rec.LastSeen = seenAt
	rec.HasValue = true

	s.items[id] = rec
	return rec.generation, nil
}

// Evict removes clients whose LastSeen is older than ttl and returns their IDs.
// Both idle and capacity eviction notify the attached detection pipeline.
func (s *Store) Evict(ttl time.Duration) []string {
	if ttl <= 0 {
		return nil
	}
	cutoff := time.Now().Add(-ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for id, rec := range s.items {
		if !rec.LastSeen.IsZero() && rec.LastSeen.Before(cutoff) {
			s.removeLocked(id)
			removed = append(removed, id)
		}
	}
	return removed
}

// Snapshot returns a sorted copy for rendering without holding the lock.
func (s *Store) Snapshot() []ClientRecord {
	s.mu.RLock()
	out := make([]ClientRecord, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type == out[j].Type {
			return out[i].ID < out[j].ID
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// removeLocked is the common lifecycle path for capacity and idle eviction.
// The hook only signals; it must not block or call back into the store.
func (s *Store) removeLocked(id string) {
	delete(s.items, id)
	s.evictionRevision++
	if s.onEviction != nil {
		s.onEviction()
	}
}

func (s *Store) generation(id string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.items[id].generation
}
