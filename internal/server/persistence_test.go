package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

func TestStore_LoadFromCSV(t *testing.T) {
	// Create a temporary CSV file
	tmpFile, err := os.CreateTemp("", "test_data_*.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	content := `timestamp,id,type,value,unit
2026-02-11T10:00:00Z,client1,heart_rate,70.00,BPM
2026-02-11T10:05:00Z,client1,heart_rate,75.00,BPM
2026-02-11T10:00:00Z,client2,person_temperature,36.50,C
`
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	store := NewStore(10)
	if err := store.LoadFromCSV(tmpFile.Name()); err != nil {
		t.Fatalf("LoadFromCSV failed: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(snapshot))
	}

	// Verify client1 has the LATEST value (75.00)
	foundClient1 := false
	for _, rec := range snapshot {
		if rec.ID == "client1" {
			foundClient1 = true
			if rec.Value != "75.00" {
				t.Errorf("expected value 75.00 for client1, got %s", rec.Value)
			}
			expectedTime, _ := time.Parse(time.RFC3339Nano, "2026-02-11T10:05:00Z")
			if !rec.DataAt.Equal(expectedTime) {
				t.Errorf("expected time %v, got %v", expectedTime, rec.DataAt)
			}
		}
	}
	if !foundClient1 {
		t.Error("client1 not found in store")
	}
}

// TestStore_LoadFromCSV_RoundTripsSanitizedFields verifies that the quoting the
// CSV logger applies to defend spreadsheet consumers against formula injection
// is undone on hydration. A negative reading is the case that matters: left
// quoted it no longer parses as a number, silently disabling threshold checks
// and the dashboard's numeric column for that client after a restart.
func TestStore_LoadFromCSV_RoundTripsSanitizedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.csv")
	logger, err := NewCSVLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC)
	logger.Append(at, "=outdoor", protocol.DataAmbientTemp, "-3.50", "C")
	logger.Close()

	store := NewStore(10)
	if err := store.LoadFromCSV(path); err != nil {
		t.Fatalf("LoadFromCSV failed: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected 1 record, got %d", len(snapshot))
	}
	if snapshot[0].Value != "-3.50" {
		t.Errorf("value = %q, want %q", snapshot[0].Value, "-3.50")
	}
	if snapshot[0].ID != "=outdoor" {
		t.Errorf("id = %q, want %q", snapshot[0].ID, "=outdoor")
	}
}

func TestStore_LoadFromCSV_Empty(t *testing.T) {
	store := NewStore(10)
	if err := store.LoadFromCSV(""); err != nil {
		t.Errorf("expected no error for empty path, got %v", err)
	}
}

func TestStore_LoadFromCSV_NonExistent(t *testing.T) {
	store := NewStore(10)
	if err := store.LoadFromCSV("non_existent_file.csv"); err != nil {
		t.Errorf("expected no error for non-existent file, got %v", err)
	}
}

// TestStore_LoadFromCSV_InvalidTimestampSkipped verifies that rows with
// unparseable timestamps are silently skipped while surrounding valid rows
// are still loaded. (Rows with wrong field counts are a distinct case: the
// csv.Reader enforces field-count consistency by default and returns an error
// for such rows rather than passing them to the application.)
func TestStore_LoadFromCSV_InvalidTimestampSkipped(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_malformed_*.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	// Row 2 has an invalid timestamp and must be skipped; rows 1 and 3 are valid.
	content := "timestamp,id,type,value,unit\n" +
		"2026-02-11T10:00:00Z,client1,heart_rate,70.00,BPM\n" +
		"not-a-timestamp,client2,heart_rate,75.00,BPM\n" +
		"2026-02-11T10:01:00Z,client3,ambient_temperature,22.00,C\n"

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	store := NewStore(10)
	if err := store.LoadFromCSV(tmpFile.Name()); err != nil {
		t.Fatalf("LoadFromCSV returned unexpected error: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 valid clients, got %d", len(snapshot))
	}

	ids := make(map[string]bool)
	for _, r := range snapshot {
		ids[r.ID] = true
	}
	if !ids["client1"] {
		t.Error("expected client1 to be loaded")
	}
	if !ids["client3"] {
		t.Error("expected client3 to be loaded")
	}
	if ids["client2"] {
		t.Error("expected client2 (invalid timestamp) to be skipped")
	}
}

// TestStore_LoadFromCSV_InvalidTypeSkipped verifies rows whose type is not a
// known protocol.DataType are dropped during hydration.
func TestStore_LoadFromCSV_InvalidTypeSkipped(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_badtype_*.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	content := "timestamp,id,type,value,unit\n" +
		"2026-02-11T10:00:00Z,client1,heart_rate,70.00,BPM\n" +
		"2026-02-11T10:00:00Z,evil,not_a_real_type,boom,X\n"

	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	store := NewStore(10)
	if err := store.LoadFromCSV(tmpFile.Name()); err != nil {
		t.Fatalf("LoadFromCSV returned unexpected error: %v", err)
	}

	snapshot := store.Snapshot()
	if len(snapshot) != 1 || snapshot[0].ID != "client1" {
		t.Fatalf("expected only client1 to load, got %d records: %+v", len(snapshot), snapshot)
	}
}

// TestStore_LoadFromCSV_RespectsMaxItems verifies hydration cannot grow the
// store past its capacity, even with more distinct clients in the log.
func TestStore_LoadFromCSV_RespectsMaxItems(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_overflow_*.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	var b strings.Builder
	b.WriteString("timestamp,id,type,value,unit\n")
	for i := range 10 {
		fmt.Fprintf(&b, "2026-02-11T10:0%d:00Z,client%d,heart_rate,70.00,BPM\n", i, i)
	}
	if _, err := tmpFile.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	store := NewStore(3)
	if err := store.LoadFromCSV(tmpFile.Name()); err != nil {
		t.Fatalf("LoadFromCSV returned unexpected error: %v", err)
	}

	if got := len(store.Snapshot()); got != 3 {
		t.Fatalf("expected store capped at 3, got %d", got)
	}
}
