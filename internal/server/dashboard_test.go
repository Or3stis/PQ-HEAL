package server

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFormatServerURL(t *testing.T) {
	cases := map[string]string{
		":9000":              "http://localhost:9000",
		"localhost:9000":     "http://localhost:9000",
		"http://example":     "http://example",
		"https://example":    "https://example",
		"127.0.0.1:8080":     "http://127.0.0.1:8080",
		"example.com:9000":   "http://example.com:9000",
		"[::1]:9000":         "http://[::1]:9000",
		"http://[::1]:9000":  "http://[::1]:9000",
		"https://[::1]:9000": "https://[::1]:9000",
	}

	for input, expected := range cases {
		if got := formatServerURL(input); got != expected {
			t.Fatalf("expected %q for %q, got %q", expected, input, got)
		}
	}
}

func TestColorizeStatus(t *testing.T) {
	const (
		colorGreen  = "\033[32m"
		colorDim    = "\033[2m"
		colorDimRed = "\033[2;31m"
	)

	now := time.Now()
	aliveWindow := 10 * time.Second

	word, color := statusParts(true, now, now, aliveWindow)
	if color != colorGreen || word != "alive" {
		t.Fatalf("expected green alive status, got word=%q color=%q", word, color)
	}

	word, color = statusParts(false, now.Add(-15*time.Second), now, aliveWindow)
	if color != colorDim || word != "stale" {
		t.Fatalf("expected dim stale status, got word=%q color=%q", word, color)
	}

	word, color = statusParts(false, now.Add(-40*time.Second), now, aliveWindow)
	if color != colorDimRed || word != "stale" {
		t.Fatalf("expected dim-red stale status, got word=%q color=%q", word, color)
	}

	word, color = statusParts(false, time.Time{}, now, aliveWindow)
	if color != "" || word != "pend" {
		t.Fatalf("expected pend with no color for zero time, got word=%q color=%q", word, color)
	}
}

func TestRenderDashboardStopsOnCancel(t *testing.T) {
	store := NewStore(DefaultMaxClients)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		RenderDashboard(ctx, store, ":9000", DefaultAliveWindow)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RenderDashboard did not stop after context cancellation")
	}
}

func TestAgeString(t *testing.T) {
	now := time.Date(2026, 2, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "-"},                   // zero time → dash
		{now.Add(1 * time.Second), "0s"},     // future time → "0s"
		{now.Add(-5 * time.Second), "5s"},    // 5 seconds ago
		{now.Add(-59 * time.Second), "59s"},  // just under a minute
		{now.Add(-60 * time.Second), "1m"},   // exactly 1 minute
		{now.Add(-90 * time.Second), "1m"},   // 1.5 min → truncated to 1m
		{now.Add(-3600 * time.Second), "1h"}, // exactly 1 hour
		{now.Add(-24 * time.Hour), "1d"},     // exactly 1 day
		{now.Add(-48 * time.Hour), "2d"},     // 2 days
	}
	for _, tc := range cases {
		if got := ageString(now, tc.t); got != tc.want {
			t.Errorf("ageString(now, %v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		max  int
		want string
	}{
		{"hello", 10, "hello"},         // shorter than max → unchanged
		{"hello", 5, "hello"},          // exactly max → unchanged
		{"hello world", 8, "hello..."}, // longer → truncated with ellipsis
		{"hello world", 5, "he..."},    // short max
		{"", 5, ""},                    // empty string
	}
	for _, tc := range cases {
		if got := truncate(tc.s, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
		}
	}
}

func TestStatusParts(t *testing.T) {
	now := time.Now()
	aliveWindow := 10 * time.Second
	const (
		green  = "\033[32m"
		dim    = "\033[2m"
		dimRed = "\033[2;31m"
	)

	word, color := statusParts(true, now, now, aliveWindow)
	if word != "alive" || color != green {
		t.Errorf("alive: got word=%q color=%q, want %q %q", word, color, "alive", green)
	}

	// Zero lastSeen → pend with no color.
	word, color = statusParts(false, time.Time{}, now, aliveWindow)
	if word != "pend" || color != "" {
		t.Errorf("zero lastSeen: got word=%q color=%q, want pend no-color", word, color)
	}

	// Within 3×aliveWindow → dim stale.
	word, color = statusParts(false, now.Add(-2*aliveWindow), now, aliveWindow)
	if word != "stale" || color != dim {
		t.Errorf("within 3x window: got word=%q color=%q, want stale dim", word, color)
	}

	// Beyond 3×aliveWindow → dim-red stale.
	word, color = statusParts(false, now.Add(-4*aliveWindow), now, aliveWindow)
	if word != "stale" || color != dimRed {
		t.Errorf("beyond 3x window: got word=%q color=%q, want stale dim-red", word, color)
	}
}

func TestColorPad(t *testing.T) {
	const (
		red   = "\033[31m"
		reset = "\033[0m"
	)

	t.Run("no color shorter than width", func(t *testing.T) {
		if got := colorPad("hi", "", 5); got != "hi   " {
			t.Errorf("got %q, want %q", got, "hi   ")
		}
	})
	t.Run("no color exactly width", func(t *testing.T) {
		if got := colorPad("hello", "", 5); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})
	t.Run("no color longer than width", func(t *testing.T) {
		if got := colorPad("toolong", "", 5); got != "toolo" {
			t.Errorf("got %q, want %q", got, "toolo")
		}
	})
	t.Run("with color shorter than width", func(t *testing.T) {
		want := red + "hi" + reset + "   "
		if got := colorPad("hi", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("with color exactly width", func(t *testing.T) {
		want := red + "hello" + reset
		if got := colorPad("hello", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("with color longer than width", func(t *testing.T) {
		want := red + "toolo" + reset
		if got := colorPad("toolong", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// Column widths are visible widths, so padding and truncation count runes.
// Measuring bytes would under-pad any multi-byte value and could cut one in
// half, corrupting the row and every column after it.
func TestColorPadIsRuneSafe(t *testing.T) {
	t.Run("pads by rune count", func(t *testing.T) {
		// "θερμ" is 4 runes but 8 bytes.
		if got := colorPad("θερμ", "", 6); got != "θερμ  " {
			t.Errorf("got %q, want %q", got, "θερμ  ")
		}
	})
	t.Run("truncates on a rune boundary", func(t *testing.T) {
		got := colorPad("θερμοκρασία", "", 5)
		if got != "θερμο" {
			t.Errorf("got %q, want %q", got, "θερμο")
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncation produced invalid UTF-8: %q", got)
		}
	})
	t.Run("right-aligned pads by rune count", func(t *testing.T) {
		if got := colorPadRight("θερμ", "", 6); got != "  θερμ" {
			t.Errorf("got %q, want %q", got, "  θερμ")
		}
	})
}

func TestColorPadRight(t *testing.T) {
	const (
		red   = "\033[31m"
		reset = "\033[0m"
	)

	t.Run("no color shorter than width", func(t *testing.T) {
		if got := colorPadRight("hi", "", 5); got != "   hi" {
			t.Errorf("got %q, want %q", got, "   hi")
		}
	})
	t.Run("no color exactly width", func(t *testing.T) {
		if got := colorPadRight("hello", "", 5); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})
	t.Run("no color longer than width", func(t *testing.T) {
		if got := colorPadRight("toolong", "", 5); got != "toolo" {
			t.Errorf("got %q, want %q", got, "toolo")
		}
	})
	t.Run("with color shorter than width", func(t *testing.T) {
		want := "   " + red + "hi" + reset
		if got := colorPadRight("hi", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("with color exactly width", func(t *testing.T) {
		want := red + "hello" + reset
		if got := colorPadRight("hello", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("with color longer than width", func(t *testing.T) {
		want := red + "toolo" + reset
		if got := colorPadRight("toolong", red, 5); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestGetSummaryString(t *testing.T) {
	// Zero total → empty string.
	if got := getSummaryString(typeSummary{}); got != "" {
		t.Errorf("expected empty string for zero total, got %q", got)
	}

	// Clients with no numeric values → avg="-".
	s := getSummaryString(typeSummary{total: 3, alive: 2, label: "X"})
	if !strings.Contains(s, "avg=-") {
		t.Errorf("expected avg=- for no numeric values, got %q", s)
	}
	if !strings.Contains(s, "count=3") {
		t.Errorf("expected count=3, got %q", s)
	}

	// Numeric values → computed average and alive percentage.
	s = getSummaryString(typeSummary{total: 2, alive: 1, sum: 100, numericCount: 2, label: "X"})
	if !strings.Contains(s, "avg=50.00") {
		t.Errorf("expected avg=50.00, got %q", s)
	}
	if !strings.Contains(s, "alive=50%") {
		t.Errorf("expected alive=50%%, got %q", s)
	}
}

func TestDashboardFrameDoesNotScroll(t *testing.T) {
	for _, rows := range []int{1, 5, 24, 80} {
		frame := dashboardFrame(strings.Repeat("client reading\n", 100), []string{"alert one", "alert two"}, rows)
		if strings.ContainsAny(frame, "\n\r") {
			t.Fatalf("%d rows: scrolling control character in frame", rows)
		}
		if got := strings.Count(frame, "\033[2K"); got != rows {
			t.Fatalf("got %d screen rows, want %d", got, rows)
		}
		if !strings.Contains(frame, "more rows") {
			t.Fatal("overflow must be indicated")
		}
	}
}

func TestDashboardFrameClearsShrunkenContent(t *testing.T) {
	frame := dashboardFrame("new reading\n", nil, 24)
	if strings.Count(frame, "\033[2K") != 24 {
		t.Fatal("every row must clear old content")
	}
	if !strings.Contains(frame, "\033[21;1H\033[0m\033[2KRecent events") {
		t.Fatal("log area must remain at a fixed position")
	}
}

func TestDashboardLogsBoundedAndSafe(t *testing.T) {
	logs := &DashboardLogs{}
	for i := 0; i < 100; i++ {
		_, _ = logs.Write([]byte("event\033[2J\r\n"))
	}
	lines := logs.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("got %d logs", len(lines))
	}
	if strings.ContainsAny(strings.Join(lines, ""), "\033\r\n") {
		t.Fatal("log control characters escaped into the screen")
	}
	lines[0] = "changed"
	if logs.Snapshot()[0] == "changed" {
		t.Fatal("snapshot aliases mutable log storage")
	}
}
