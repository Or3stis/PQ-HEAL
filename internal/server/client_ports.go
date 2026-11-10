package server

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var clientPortHeader = []string{"timestamp", "client_id", "endpoint", "client_ip", "client_port", "request_id"}

// ClientPortLogger records accepted requests using the transport peer address,
// never client-supplied forwarding headers. It is separate from telemetry history.
type ClientPortLogger struct {
	mu     sync.Mutex
	file   *os.File
	writer *csv.Writer
}

func NewClientPortLogger(path string) (*ClientPortLogger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	w := csv.NewWriter(f)
	if info.Size() == 0 {
		err = w.Write(clientPortHeader)
		w.Flush()
		if err == nil {
			err = w.Error()
		}
	} else {
		var header []string
		header, err = csv.NewReader(f).Read()
		if err == nil && !slices.Equal(header, clientPortHeader) {
			err = fmt.Errorf("client port log has an incompatible CSV header: %s", path)
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &ClientPortLogger{file: f, writer: w}, nil
}

func (l *ClientPortLogger) Append(at time.Time, id, endpoint, remoteAddr, requestID string) {
	if l == nil {
		return
	}
	ip, port, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip, port = "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.writer.Write([]string{at.UTC().Format(time.RFC3339Nano), sanitizeCSVField(id), endpoint, sanitizeCSVField(ip), port, sanitizeCSVField(requestID)})
	l.writer.Flush()
	if err := l.writer.Error(); err != nil {
		slog.Error("client port CSV flush", "err", err)
	}
}

func (l *ClientPortLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writer.Flush()
	if err := l.writer.Error(); err != nil {
		slog.Error("client port CSV flush", "err", err)
	}
	if err := l.file.Close(); err != nil {
		slog.Error("client port CSV close", "err", err)
	}
}
