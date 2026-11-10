package server

import (
	"encoding/csv"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"pq-heal/internal/protocol"
)

type CSVLogger struct {
	mu     sync.Mutex
	file   *os.File
	writer *csv.Writer
}

// NewCSVLogger opens/creates the CSV file and writes a header if it's new.
func NewCSVLogger(path string) (*CSVLogger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	logger := &CSVLogger{
		file:   file,
		writer: csv.NewWriter(file),
	}

	if info.Size() == 0 {
		_ = logger.writer.Write([]string{"timestamp", "id", "type", "value", "unit"})
		logger.writer.Flush()
	}

	return logger, nil
}

// Append writes a single telemetry row, guarding the writer with a mutex.
func (l *CSVLogger) Append(dataAt time.Time, id string, dataType protocol.DataType, value string, unit string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	_ = l.writer.Write([]string{
		dataAt.UTC().Format(time.RFC3339Nano),
		sanitizeCSVField(id),
		sanitizeCSVField(string(dataType)),
		sanitizeCSVField(value),
		sanitizeCSVField(unit),
	})
	l.writer.Flush()
	if err := l.writer.Error(); err != nil {
		slog.Error("csv logger flush", "err", err)
	}
	l.mu.Unlock()
}

// Close flushes buffered writes and closes the underlying file.
func (l *CSVLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.writer.Flush()
	_ = l.file.Close()
	l.mu.Unlock()
}

// sanitizeCSVField prevents spreadsheet formula injection on CSV consumers.
func sanitizeCSVField(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@':
		return "'" + v
	default:
		return v
	}
}

// unsanitizeCSVField is the inverse of sanitizeCSVField, applied when the log is
// read back. Without it a negative reading written as "'-3.50" hydrates as that
// literal string: it no longer parses as a number, so threshold checks and the
// dashboard's numeric column silently skip it.
func unsanitizeCSVField(v string) string {
	if len(v) < 2 || v[0] != '\'' {
		return v
	}
	switch v[1] {
	case '=', '+', '-', '@':
		return v[1:]
	default:
		return v
	}
}
