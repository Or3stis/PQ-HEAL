package client

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"pq-heal/internal/protocol"
)

func PromptNonEmpty(reader *bufio.Reader, label string) string {
	// Loop until we get a non-empty response.
	for {
		fmt.Print(label)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
		fmt.Println("Value required.")
	}
}

func PromptType(reader *bufio.Reader) protocol.DataType {
	// Accept either a number or the literal type name.
	options := protocol.Types()
	for {
		fmt.Println("Select client data type:")
		for i, opt := range options {
			fmt.Printf("  %d) %s\n", i+1, protocol.DisplayNameForType(opt))
		}
		fmt.Print("Choice: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			fmt.Println("Choice required.")
			continue
		}

		if idx, err := strconv.Atoi(line); err == nil {
			if idx >= 1 && idx <= len(options) {
				return options[idx-1]
			}
		}

		for _, opt := range options {
			if line == string(opt) {
				return opt
			}
		}

		fmt.Println("Invalid choice.")
	}
}

func PromptFrequency(reader *bufio.Reader) time.Duration {
	// Accept "2s" or a bare number (seconds).
	for {
		fmt.Print("Data transmission frequency (e.g. 2s, 500ms, or 5 for seconds): ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Println("Frequency required.")
			continue
		}
		freq, err := ParseFrequency(line)
		if err != nil || freq <= 0 {
			fmt.Println("Invalid frequency.")
			continue
		}
		return freq
	}
}

func ParseFrequency(input string) (time.Duration, error) {
	// time.ParseDuration handles suffixed values; fallback to seconds.
	if strings.ContainsAny(input, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return time.ParseDuration(input)
	}

	f, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(f * float64(time.Second)), nil
}
