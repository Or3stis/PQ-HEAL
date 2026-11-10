package server

import (
	"encoding/csv"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientPortsAcceptedRequests(t *testing.T) {
	for _, endpoint := range []string{"/ingest", "/heartbeat"} {
		for _, peer := range []struct{ addr, ip, port string }{{"192.0.2.1:54321", "192.0.2.1", "54321"}, {"[2001:db8::1]:65432", "2001:db8::1", "65432"}, {"unavailable", "", ""}} {
			t.Run(endpoint+peer.addr, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "ports.csv")
				logger, err := NewClientPortLogger(path)
				if err != nil {
					t.Fatal(err)
				}
				defer logger.Close()
				h, err := NewHandler(HandlerConfig{Store: NewStore(100), ClientPorts: logger, APIKey: "test"})
				if err != nil {
					t.Fatal(err)
				}
				payload := fmt.Sprintf(`{"id":"sensor","type":"heart_rate","timestamp":%q`, time.Now().UTC().Format(time.RFC3339Nano))
				if endpoint == "/ingest" {
					payload += `,"value":72`
				}
				payload += "}"
				request := func(auth bool) *httptest.ResponseRecorder {
					r := httptest.NewRequest("POST", endpoint, strings.NewReader(payload))
					r.RemoteAddr = peer.addr
					r.Header.Set("X-Forwarded-For", "203.0.113.1:1")
					r.Header.Set("Forwarded", "for=203.0.113.1:1")
					if auth {
						r.Header.Set("Authorization", "Bearer test")
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w
				}
				if w := request(false); w.Code != 401 {
					t.Fatalf("unauthorized=%d", w.Code)
				}
				w := request(true)
				if w.Code != 202 {
					t.Fatalf("accepted=%d %s", w.Code, w.Body.String())
				}
				if w := request(true); w.Code != 400 {
					t.Fatalf("replay=%d", w.Code)
				}
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				rows, err := csv.NewReader(f).ReadAll()
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 2 {
					t.Fatalf("only accepted request should be logged: %v", rows)
				}
				r := rows[1]
				if r[1] != "sensor" || r[2] != endpoint || r[3] != peer.ip || r[4] != peer.port || r[5] != w.Header().Get("X-Request-ID") {
					t.Fatalf("wrong peer/correlation: %v", r)
				}
				if _, err := time.Parse(time.RFC3339Nano, r[0]); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestClientPortLoggerAppendAndConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.csv")
	for pass := 0; pass < 2; pass++ {
		l, err := NewClientPortLogger(path)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); l.Append(time.Now(), "=sensor", "/ingest", "127.0.0.1:50000", "request") }()
		}
		wg.Wait()
		l.Close()
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 101 || rows[1][1] != "'=sensor" {
		t.Fatalf("invalid append results: rows=%d", len(rows))
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestClientPortLoggerDisabledAndWrongHeader(t *testing.T) {
	l, err := NewClientPortLogger("")
	if err != nil || l != nil {
		t.Fatal("empty path should disable")
	}
	l.Append(time.Now(), "", "", "", "")
	l.Close()
	path := filepath.Join(t.TempDir(), "data.csv")
	before := []byte("timestamp,id,type,value,unit\n")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if l, err := NewClientPortLogger(path); err == nil {
		l.Close()
		t.Fatal("must reject telemetry CSV")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("existing file changed")
	}
}
