package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pq-heal/internal/protocol"
)

func TestParseFrequency(t *testing.T) {
	freq, err := ParseFrequency("2s")
	if err != nil || freq.String() != "2s" {
		t.Fatalf("expected 2s, got %v (%v)", freq, err)
	}

	freq, err = ParseFrequency("1.5")
	if err != nil || freq.String() != "1.5s" {
		t.Fatalf("expected 1.5s, got %v (%v)", freq, err)
	}
}

func TestValueGeneratorDifferent(t *testing.T) {
	gen := NewValueGenerator(protocol.DataHeartRate)
	first := gen.Next().(float64)
	second := gen.Next().(float64)
	if first == second {
		t.Fatalf("expected different values, got %v and %v", first, second)
	}

	motion := NewValueGenerator(protocol.DataMotion)
	mFirst := motion.Next().(bool)
	mSecond := motion.Next().(bool)
	if mFirst == mSecond {
		t.Fatalf("expected different motion values, got %v and %v", mFirst, mSecond)
	}
}

func TestPostJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	p := payload{Name: "test"}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("expected Bearer secret, got %s", r.Header.Get("Authorization"))
		}

		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		if body.Name != "test" {
			t.Errorf("expected name=test, got %s", body.Name)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	err := PostJSON(http.DefaultClient, ts.URL, p, "secret")
	if err != nil {
		t.Fatalf("PostJSON failed: %v", err)
	}
}

func TestPostJSONError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("error message"))
	}))
	defer ts.Close()

	err := PostJSON(http.DefaultClient, ts.URL, map[string]string{"foo": "bar"}, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "error message") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSenderIncludesSubjectInIngest(t *testing.T) {
	var got IngestPayload
	requests := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	sender := NewSender("heart-1", protocol.DataHeartRate, "BPM", false, NewValueGenerator(protocol.DataHeartRate), nil, &http.Client{Transport: transport}, "http://localhost", "")
	sender.SubjectID = "patient-1"
	sender.SendData()
	if requests != 1 || got.ID != "heart-1" || got.SubjectID != "patient-1" {
		t.Fatalf("unexpected transmitted identity: %+v", got)
	}
}
