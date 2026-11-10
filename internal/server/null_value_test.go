package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"pq-heal/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestNullMeasurementsRejectedThroughAPI(t *testing.T) {
	store := NewStore(10)
	pipe := NewDetectionPipeline(nil, &captureSink{}, 8)
	handler, err := NewHandler(HandlerConfig{Store: store, Detection: pipe})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range protocol.Types() {
		body := fmt.Sprintf(`{"id":"sensor","type":%q,"value": null ,"timestamp":%q}`, typ, time.Now().UTC().Format(time.RFC3339Nano))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body)))
		if response.Code != 400 || !strings.Contains(response.Body.String(), `"code":"value_required"`) {
			t.Fatalf("%s: %d %s", typ, response.Code, response.Body.String())
		}
	}
	if len(store.Snapshot()) != 0 || len(pipe.in) != 0 {
		t.Fatal("invalid values reached storage/detection")
	}
	// Numeric zero and boolean false remain legitimate values.
	postReviewReading(t, handler, "temperature", "", "ambient_temperature", "0", time.Now())
	postReviewReading(t, handler, "motion", "", "motion", "false", time.Now())
}
