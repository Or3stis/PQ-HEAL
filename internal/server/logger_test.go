package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pq-heal/internal/protocol"
)

func TestCSVLoggerHeaderOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.csv")

	logger, err := NewCSVLogger(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatalf("expected logger instance")
	}
	logger.Close()

	// Reopen and append a row; header should only appear once.
	logger, err = NewCSVLogger(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	now := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)
	logger.Append(now, "client-1", protocol.DataHeartRate, "72.00", "BPM")
	logger.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (header + row), got %d", len(lines))
	}
	if lines[0] != "timestamp,id,type,value,unit" {
		t.Fatalf("unexpected header: %q", lines[0])
	}
}

func TestSanitizeCSVField(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"safe":     "safe",
		"=formula": "'=formula",
		"+plus":    "'+plus",
		"-minus":   "'-minus",
		"@at":      "'@at",
	}

	for input, expected := range cases {
		if got := sanitizeCSVField(input); got != expected {
			t.Fatalf("expected %q for %q, got %q", expected, input, got)
		}
	}
}
