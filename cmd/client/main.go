package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"pq-heal/internal/client"
	"pq-heal/internal/protocol"
)

func main() {
	// Flags to configure where to send data and how often to send the heartbeat.
	server := flag.String("server", "http://localhost:9000", "server base URL")
	heartbeatEvery := flag.Duration("heartbeat", 5*time.Second, "heartbeat interval")
	timeout := flag.Duration("timeout", 5*time.Second, "HTTP timeout")
	apiKey := flag.String("api-key", "", "API key for ingest/heartbeat (defaults to PQHEAL_API_KEY)")
	encrypt := flag.Bool("encrypt", false, "send /ingest and /heartbeat as encrypted envelopes")
	keyPin := flag.String("key-pin", "", "expected SHA-256 fingerprint for /keys bundle (required with -encrypt)")
	bundleTTL := flag.Duration("key-ttl", time.Hour, "refresh the cached /keys bundle after this duration (used with -encrypt)")

	// New flags to allow bypassing interactive prompts.
	subjectID := flag.String("subject-id", "", "shared subject identity for related sensors")
	idFlag := flag.String("id", "", "unique client ID")
	typeFlag := flag.String("type", "", "data type (heart_rate, person_temperature, etc.)")
	freqFlag := flag.String("frequency", "", "transmission frequency (e.g. 2s, 5s)")

	flag.Parse()
	if *apiKey == "" {
		*apiKey = os.Getenv("PQHEAL_API_KEY")
	}
	if *encrypt && strings.TrimSpace(*keyPin) == "" {
		fmt.Fprintln(os.Stderr, "missing -key-pin; encrypted mode requires trusted /keys fingerprint pinning")
		os.Exit(2)
	}

	reader := bufio.NewReader(os.Stdin)

	// Use flag values if provided, otherwise prompt.
	id := *idFlag
	if id == "" {
		id = client.PromptNonEmpty(reader, "Client ID: ")
	}

	var dataType protocol.DataType
	if *typeFlag != "" {
		dataType = protocol.DataType(*typeFlag)
		if !protocol.IsValidType(dataType) {
			fmt.Printf("Invalid type '%s'. Falling back to prompt.\n", *typeFlag)
			dataType = client.PromptType(reader)
		}
	} else {
		dataType = client.PromptType(reader)
	}

	var frequency time.Duration
	if *freqFlag != "" {
		parsed, err := client.ParseFrequency(*freqFlag)
		if err != nil || parsed <= 0 {
			fmt.Printf("Invalid frequency '%s'. Falling back to prompt.\n", *freqFlag)
			frequency = client.PromptFrequency(reader)
		} else {
			frequency = parsed
		}
	} else {
		frequency = client.PromptFrequency(reader)
	}

	// The unit and data generator are derived from the chosen type.
	unit := protocol.UnitForType(dataType)
	generator := client.NewValueGenerator(dataType)

	fmt.Printf("\nSending %s (%s) every %s to %s\n", dataType, unit, frequency, *server)
	fmt.Printf("Heartbeat every %s | Type 'quit' or Ctrl+C to stop.\n", *heartbeatEvery)

	// HTTP client is reused for all requests to keep things efficient.
	clientHTTP, err := client.NewHTTPClient(*timeout, os.Getenv("SSL_CERT_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	securePoster := client.NewSecurePoster(clientHTTP, *server, *apiKey, *keyPin, *bundleTTL)

	sender := client.NewSender(id, dataType, unit, *encrypt, generator, securePoster, clientHTTP, *server, *apiKey)

	sender.SubjectID = strings.TrimSpace(*subjectID)

	// Listen for Ctrl+C and OS signals to stop cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Read stdin in the background so "quit" can be typed while tickers run.
	// Reuse the prompt reader rather than opening a second bufio over os.Stdin:
	// a fresh reader would miss any bytes the prompt reader already buffered.
	valueUpdates := make(chan string)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if t := strings.TrimSpace(line); t != "" {
				valueUpdates <- t
			}
			if err != nil {
				return
			}
		}
	}()

	// Tick on both data and heartbeat intervals.
	dataTicker := time.NewTicker(frequency)
	heartbeatTicker := time.NewTicker(*heartbeatEvery)
	defer dataTicker.Stop()
	defer heartbeatTicker.Stop()

	// Send an initial heartbeat and data point immediately, then loop.
	sender.SendHeartbeat()
	sender.SendData()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nclient stopped")
			return
		case line := <-valueUpdates:
			if line == "" {
				continue
			}
			if strings.EqualFold(line, "quit") {
				fmt.Println("\nclient stopped")
				return
			}
		case <-dataTicker.C:
			sender.SendData()
		case <-heartbeatTicker.C:
			sender.SendHeartbeat()
		}
	}
}
