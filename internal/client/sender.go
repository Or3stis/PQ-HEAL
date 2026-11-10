package client

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"pq-heal/internal/protocol"
)

type IngestPayload struct {
	SubjectID string            `json:"subject_id,omitempty"`
	ID        string            `json:"id"`
	Type      protocol.DataType `json:"type"`
	Value     any               `json:"value"`
	Unit      string            `json:"unit,omitempty"`
	Timestamp *time.Time        `json:"timestamp,omitempty"`
}

type HeartbeatPayload = protocol.HeartbeatPayload

type Sender struct {
	// SubjectID links this sensor to related sensors; configure before sending.
	SubjectID    string
	id           string
	dataType     protocol.DataType
	unit         string
	encrypt      bool
	generator    *ValueGenerator
	securePoster *SecurePoster
	httpClient   *http.Client
	server       string
	apiKey       string
}

func NewSender(id string, dataType protocol.DataType, unit string, encrypt bool,
	generator *ValueGenerator, securePoster *SecurePoster,
	httpClient *http.Client, server, apiKey string) *Sender {
	return &Sender{
		id:           id,
		dataType:     dataType,
		unit:         unit,
		encrypt:      encrypt,
		generator:    generator,
		securePoster: securePoster,
		httpClient:   httpClient,
		server:       server,
		apiKey:       apiKey,
	}
}

func (s *Sender) SendHeartbeat() {
	now := time.Now().UTC()
	payload := HeartbeatPayload{
		ID:        s.id,
		Type:      s.dataType,
		Status:    "alive",
		Timestamp: &now,
	}
	var err error
	if s.encrypt {
		err = s.securePoster.Post("/heartbeat", payload, s.id, now)
	} else {
		err = PostJSON(s.httpClient, s.server+"/heartbeat", payload, s.apiKey)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat error: %v\n", err)
	}
}

func (s *Sender) SendData() {
	now := time.Now().UTC()
	val := s.generator.Next()
	payload := IngestPayload{
		SubjectID: s.SubjectID,
		ID:        s.id,
		Type:      s.dataType,
		Value:     val,
		Unit:      s.unit,
		Timestamp: &now,
	}
	var err error
	if s.encrypt {
		err = s.securePoster.Post("/ingest", payload, s.id, now)
	} else {
		err = PostJSON(s.httpClient, s.server+"/ingest", payload, s.apiKey)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "data error: %v\n", err)
	} else {
		fmt.Printf("[sent] %s=%v %s\n", s.dataType, val, s.unit)
	}
}
