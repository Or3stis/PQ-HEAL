package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"pq-heal/internal/secure"
)

// --- authorizeRequest ---

func TestAuthorizeRequest(t *testing.T) {
	const apiKey = "my-secret"

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"correct token", "Bearer " + apiKey, true},
		{"missing header", "", false},
		{"no Bearer prefix", apiKey, false},
		{"lowercase bearer", "bearer " + apiKey, false},
		{"wrong token", "Bearer wrong-key", false},
		// TrimSpace strips the trailing space, leaving "Bearer" which fails HasPrefix("Bearer ")
		{"empty token after Bearer", "Bearer ", false},
		// TrimSpace collapses surrounding whitespace; inner value is still correct
		{"surrounding whitespace", "  Bearer " + apiKey + "  ", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := authorizeRequest(r, apiKey); got != tc.want {
				t.Fatalf("authorizeRequest(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// --- readBodyLimited ---

func TestReadBodyLimitedAcceptsWithinLimit(t *testing.T) {
	const limit = 32 << 10
	body := bytes.Repeat([]byte("x"), limit-1)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	w := httptest.NewRecorder()

	got, err := readBodyLimited(w, r, limit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("expected %d bytes, got %d", len(body), len(got))
	}
}

func TestReadBodyLimitedRejectsOversizedBody(t *testing.T) {
	const limit = 32 << 10
	body := bytes.Repeat([]byte("x"), limit+1)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	w := httptest.NewRecorder()

	_, err := readBodyLimited(w, r, limit)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
}

// --- secure.DecodeStrictJSON (used by the server handler) ---

func TestDecodeStrictJSONValid(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var p payload
	if err := secure.DecodeStrictJSON([]byte(`{"name":"test"}`), &p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "test" {
		t.Fatalf("expected name 'test', got %q", p.Name)
	}
}

func TestDecodeStrictJSONTrailingContent(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var p payload
	err := secure.DecodeStrictJSON([]byte(`{"name":"test"}{"extra":true}`), &p)
	if err != secure.ErrTrailingJSONContent {
		t.Fatalf("expected ErrTrailingJSONContent, got %v", err)
	}
}

func TestDecodeStrictJSONUnknownField(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var p payload
	if err := secure.DecodeStrictJSON([]byte(`{"name":"test","extra":1}`), &p); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestDecodeStrictJSONInvalidJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var p payload
	if err := secure.DecodeStrictJSON([]byte(`{"name":`), &p); err == nil {
		t.Fatal("expected error for truncated JSON, got nil")
	}
}
