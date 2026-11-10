package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"pq-heal/internal/protocol"
)

const DefaultAliveWindow = 10 * time.Second

// RenderDashboard updates a bounded terminal screen with current client state.
func RenderDashboard(ctx context.Context, store *Store, addr string, aliveWindow time.Duration, logs ...*DashboardLogs) {
	if !IsTerminal(os.Stdout) {
		return
	}
	fmt.Print("\033[?1049h\033[?25l\033[?7l")
	defer fmt.Print("\033[0m\033[?7h\033[?25h\033[?1049l")
	draw := func(content string) {
		_, rows := terminalSize(os.Stdout)
		if rows < 1 {
			rows = 24
		}
		var recent []string
		if len(logs) > 0 {
			recent = logs[0].Snapshot()
		}
		fmt.Print(dashboardFrame(content, recent, rows))
	}
	startedAt := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Precompute values that never change across ticks.
	serverURL := formatServerURL(addr)
	headerSep := strings.Repeat("─", 80)
	sectionSep := strings.Repeat("-", 80)
	var thresholdParts []string
	for _, dt := range protocol.Types() {
		if th, ok := protocol.Thresholds[dt]; ok {
			thresholdParts = append(thresholdParts, fmt.Sprintf("%s [%.0f-%.0f %s]", dt, th.Min, th.Max, protocol.UnitForType(dt)))
		}
	}
	thresholdLine := ""
	if len(thresholdParts) > 0 {
		thresholdLine = "Thresholds: " + strings.Join(thresholdParts, " | ") + "\n"
	}

	// Use a strings.Builder to buffer the output and reduce flicker.
	var buf strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Pull a stable snapshot so rendering doesn't hold store locks.
		snapshot := store.Snapshot()
		now := time.Now()

		buf.Reset()
		// Move cursor to top-left without clearing (prevents screen-clear flicker).

		buf.WriteString("PQ Heal Server Dashboard\n")
		buf.WriteString(fmt.Sprintf("Server: %s\n", serverURL))
		buf.WriteString(fmt.Sprintf("Listening on %s | Clients: %d | Up: %s | %s\n",
			addr,
			len(snapshot),
			ageString(now, startedAt),
			now.Format("15:04:05"),
		))
		buf.WriteString(headerSep + "\n\n")

		if len(snapshot) == 0 {
			buf.WriteString("No clients connected yet. POST JSON to /ingest or /heartbeat\n")
			draw(buf.String())
			continue
		}

		var currentType string
		var summary typeSummary
		var warnCount int

		for i, item := range snapshot {
			if i == 0 || string(item.Type) != currentType {
				if i != 0 {
					buf.WriteString(getSummaryString(summary))
				}
				currentType = string(item.Type)
				summary = typeSummary{label: strings.ToUpper(currentType)}
				buf.WriteString(fmt.Sprintf("== %s ==\n", summary.label))
				buf.WriteString(fmt.Sprintf("%-18s %-10s %-8s %-10s %-10s %-12s\n",
					"CLIENT", "VALUE", "UNIT", "DATA AGE", "SEEN AGE", "STATUS"))
				buf.WriteString(sectionSep + "\n")
			}

			valueStr := item.Value
			valueColor := ""
			unit := item.Unit
			dataAge := "-"
			statusLabel := ""

			if item.HasValue {
				dataAge = ageString(now, item.DataAt)
				if v, err := strconv.ParseFloat(item.Value, 64); err == nil {
					if !protocol.CheckThreshold(item.Type, v) {
						statusLabel = " [WARN]"
						valueColor = "\033[31m"
						warnCount++
					}
					summary.sum += v
					summary.numericCount++
				}
			} else {
				valueStr = "-"
				unit = ""
			}

			// Alive uses LastSeen so heartbeats keep the client green.
			alive := !item.LastSeen.IsZero() && now.Sub(item.LastSeen) <= aliveWindow
			statusWord, statusColor := statusParts(alive, item.LastSeen, now, aliveWindow)
			lastSeen := ageString(now, item.LastSeen)

			// STATUS cell: 5-char word + optional " [WARN]" + trailing spaces to fill 12.
			padCount := max(12-5-len(statusLabel), 0)
			statusCell := colorPad(statusWord, statusColor, 5) + statusLabel + strings.Repeat(" ", padCount)
			buf.WriteString(fmt.Sprintf("%-18s %s %-8s %-10s %-10s %s\n",
				truncate(item.ID, 18),
				colorPadRight(truncate(valueStr, 10), valueColor, 10),
				truncate(unit, 8),
				dataAge,
				lastSeen,
				statusCell,
			))

			summary.total++
			if alive {
				summary.alive++
			}
		}
		buf.WriteString(getSummaryString(summary))
		if warnCount > 0 && thresholdLine != "" {
			buf.WriteString(thresholdLine)
		}
		buf.WriteString("Keys: Ctrl+C = quit\n")
		// Erase from cursor to end of screen, clearing stale lines from longer previous renders.
		draw(buf.String())
	}
}

type typeSummary struct {
	label        string
	total        int
	alive        int
	sum          float64
	numericCount int
}

// truncate keeps IDs/units aligned in the fixed-width table. It counts and slices
// by rune so a multi-byte ID is never cut mid-rune into invalid UTF-8.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// ageString humanizes durations into short age labels.
func ageString(now time.Time, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	if now.Before(t) {
		return "0s"
	}
	d := now.Sub(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// getSummaryString returns the per-type totals and averages as a string.
func getSummaryString(summary typeSummary) string {
	if summary.total == 0 {
		return ""
	}
	alivePct := int(float64(summary.alive) / float64(summary.total) * 100.0)
	avg := "-"
	if summary.numericCount > 0 {
		avg = fmt.Sprintf("%.2f", summary.sum/float64(summary.numericCount))
	}
	return fmt.Sprintf("SUMMARY: count=%d alive=%d%% avg=%s\n\n", summary.total, alivePct, avg)
}

func formatServerURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}

// colorPad formats s left-aligned to the given visible width with an optional ANSI color.
func colorPad(s, color string, width int) string {
	return colorPadImpl(s, color, width, false)
}

// colorPadRight formats s right-aligned to the given visible width with an optional ANSI color.
func colorPadRight(s, color string, width int) string {
	return colorPadImpl(s, color, width, true)
}

// colorPadImpl measures and slices by rune, not by byte, so that a multi-byte
// value occupies the column width the caller asked for and is never cut into
// invalid UTF-8 — matching the guarantee truncate already makes.
func colorPadImpl(s, color string, width int, right bool) string {
	const reset = "\033[0m"
	if n := utf8.RuneCountInString(s); n >= width {
		if n > width {
			s = string([]rune(s)[:width])
		}
		if color == "" {
			return s
		}
		return color + s + reset
	}
	pad := strings.Repeat(" ", width-utf8.RuneCountInString(s))
	if right {
		if color == "" {
			return pad + s
		}
		return pad + color + s + reset
	}
	if color == "" {
		return s + pad
	}
	return color + s + reset + pad
}

// statusParts returns the display word and its ANSI color code.
// "alive" = green, "pend" = no color (never seen), "stale" = dim or dim-red.
func statusParts(alive bool, lastSeen time.Time, now time.Time, aliveWindow time.Duration) (word, color string) {
	if alive {
		return "alive", "\033[32m"
	}
	if lastSeen.IsZero() {
		return "pend", ""
	}
	if now.Sub(lastSeen) > 3*aliveWindow {
		return "stale", "\033[2;31m"
	}
	return "stale", "\033[2m"
}
