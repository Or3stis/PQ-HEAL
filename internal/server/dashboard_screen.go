package server

import (
	"fmt"
	"strings"
	"sync"
)

// DashboardLogs keeps recent structured log records from scrolling the UI.
// Redirect stderr to a file when a complete structured log history is needed.
type DashboardLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *DashboardLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		// Do not allow log content to move the terminal cursor.
		line = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, line)
		l.lines = append(l.lines, truncate(line, 512))
	}
	if len(l.lines) > 3 {
		l.lines = append([]string(nil), l.lines[len(l.lines)-3:]...)
	}
	return len(p), nil
}

func (l *DashboardLogs) Snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// Each row is positioned explicitly, with wrapping disabled by RenderDashboard.
// No newline is emitted, including on the bottom row, so a frame cannot scroll.
func dashboardFrame(content string, logs []string, rows int) string {
	if rows < 1 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	logRows := min(4, max(0, rows-8))
	available := rows - logRows
	if len(lines) > available {
		hidden := len(lines) - available + 1
		lines = append(lines[:available-1], fmt.Sprintf("... %d more rows; enlarge terminal | Ctrl+C = quit", hidden))
	}
	for len(lines) < available {
		lines = append(lines, "")
	}
	if logRows > 0 {
		lines = append(lines, "Recent events (redirect stderr to retain full logs):")
		if len(logs) > logRows-1 {
			logs = logs[len(logs)-(logRows-1):]
		}
		lines = append(lines, logs...)
	}
	var frame strings.Builder
	for row := 0; row < rows; row++ {
		fmt.Fprintf(&frame, "\033[%d;1H\033[0m\033[2K", row+1)
		if row < len(lines) {
			frame.WriteString(lines[row])
		}
	}
	frame.WriteString("\033[0m\033[H")
	return frame.String()
}
