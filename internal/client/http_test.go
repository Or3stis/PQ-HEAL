package client

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExplicitCATrust(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer ts.Close()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewHTTPClient(time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if c.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("verification disabled")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPClient(time.Second, path); err == nil {
		t.Fatal("invalid CA accepted")
	}
}
