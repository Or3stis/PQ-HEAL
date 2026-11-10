package server

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder captures the response status code so the access log can report
// it. It defaults to 200, matching net/http's behavior when a handler writes a
// body without calling WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// withAccessLog logs one structured line per request after it completes. The
// request_id is read back from the response header the handlers set, tying the
// access log to the X-Request-ID clients see. Only URL.Path is logged (never the
// query string) to avoid capturing values a caller may append.
func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"request_id", rec.Header().Get("X-Request-ID"),
			"remote", r.RemoteAddr,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
