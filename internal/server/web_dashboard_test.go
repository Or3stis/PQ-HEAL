package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebDashboardServesPageAndAssets(t *testing.T) {
	h := newTestHandler(t, HandlerConfig{Store: NewStore(10), APIKey: "secret"})
	for _, tc := range []struct{ path, contentType, contains string }{
		{"/dashboard", "text/html", "PQ-HEAL"},
		{"/dashboard/style.css", "text/css", "#faf9f6"},
		{"/dashboard/app.js", "javascript", "fetch('/clients'"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), tc.contentType) || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Fatalf("%s: status=%d type=%q body=%q", tc.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}
