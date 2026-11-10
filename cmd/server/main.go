package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"pq-heal/internal/detect"
	"pq-heal/internal/server"
)

const defaultPort = "9000"

func main() {
	// Parse CLI flags first so we can wire up the server configuration.
	port := flag.String("port", defaultPort, "port to listen on")
	bind := flag.String("bind", "", "interface address to bind (empty binds all; set to separate data-plane from management-plane traffic)")
	modelPath := flag.String("model", "", "path to the trained anomaly detection model (empty disables the Isolation Forest layer)")
	detectQueue := flag.Int("detect-queue", server.DefaultDetectionQueue, "readings buffered ahead of the anomaly detector")
	clientPortLogPath := flag.String("client-port-log", "client-ports.csv", "CSV of accepted client source IPs/ports (empty disables)")
	logPath := flag.String("log", "data.csv", "path to append CSV history (empty disables)")
	aliveWindow := flag.Duration("alive-window", server.DefaultAliveWindow, "duration to consider a client alive")
	maxClients := flag.Int("max-clients", server.DefaultMaxClients, "maximum number of clients to track")
	evictAfter := flag.Duration("evict-after", 0, "remove clients not seen for this long (0 disables)")
	apiKey := flag.String("api-key", "", "API key for ingest/heartbeat (defaults to PQHEAL_API_KEY)")
	allowUnauthenticated := flag.Bool("allow-unauthenticated", false, "allow ingest/heartbeat without API key (not recommended)")
	keyStorePath := flag.String("key-store", "server-keys.json", "path to persisted server encryption keyring (empty disables persistence)")
	rateLimitRPS := flag.Float64("rate-limit-rps", 0, "per-client request rate limit in requests/sec (0 disables)")
	rateLimitBurst := flag.Float64("rate-limit-burst", 10, "per-client token bucket burst size (used with -rate-limit-rps)")
	rateLimitIdle := flag.Duration("rate-limit-idle", server.DefaultIdleBucketAfter, "evict per-client rate-limit buckets idle longer than this")
	logFormat := flag.String("log-format", "text", "structured log format: text or json")
	accessLog := flag.Bool("access-log", false, "log one structured line per HTTP request")
	tlsCert := flag.String("tls-cert", "", "path to TLS certificate (enables HTTPS; requires -tls-key)")
	tlsKey := flag.String("tls-key", "", "path to TLS private key (enables HTTPS; requires -tls-cert)")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /health endpoint and exit 0 if healthy")
	flag.Parse()
	*apiKey = configuredAPIKey(*apiKey)

	configureLogger(*logFormat, os.Stderr)

	// The binary probes itself so that external monitoring can establish
	// liveness without an HTTP client being present on the edge node.
	if *healthcheck {
		os.Exit(probeHealth(*port, *tlsCert != ""))
	}

	if strings.TrimSpace(*apiKey) == "" && !*allowUnauthenticated {
		slog.Error("missing API key; set PQHEAL_API_KEY or -api-key, or -allow-unauthenticated for local/testing only")
		os.Exit(1)
	}

	// TLS is opt-in but both halves are required together. Serving auth over
	// plaintext HTTP exposes the Bearer API key on the wire (the message-level
	// envelope encrypts payloads, not the Authorization header), so warn when a
	// key is set without transport encryption and no edge terminator is assumed.
	useTLS := *tlsCert != "" || *tlsKey != ""
	if useTLS && (*tlsCert == "" || *tlsKey == "") {
		slog.Error("both -tls-cert and -tls-key are required to enable TLS")
		os.Exit(1)
	}
	if !useTLS && strings.TrimSpace(*apiKey) != "" {
		slog.Warn("serving with -api-key over plaintext HTTP; the API key is sent in cleartext — enable -tls-cert/-tls-key or terminate TLS at the edge")
	}

	// Shared server state is the store variable, and optional CSV logging is configured up front.
	addr := strings.TrimSpace(*bind) + ":" + strings.TrimPrefix(*port, ":")
	store := server.NewStore(*maxClients)

	// Hydrate store from existing CSV log if available.
	if *logPath != "" {
		if err := store.LoadFromCSV(*logPath); err != nil {
			slog.Warn("could not hydrate store from CSV", "path", *logPath, "err", err)
		} else {
			slog.Info("hydrated store from CSV", "path", *logPath)
		}
	}

	csvLogger, err := server.NewCSVLogger(*logPath)
	if err != nil {
		slog.Error("csv log error", "err", err)
		os.Exit(1)
	}
	defer func() {
		if csvLogger != nil {
			csvLogger.Close()
		}
	}()

	portLogger, err := server.NewClientPortLogger(*clientPortLogPath)
	if err != nil {
		slog.Error("client port log error", "err", err)
		os.Exit(1)
	}
	defer portLogger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The anomaly detection model is loaded once at start-up. A missing or
	// unreadable model is not fatal: the hub degrades to the threshold,
	// stream-integrity and adaptive layers and reports the degradation, which
	// is the behaviour specified in D2.2 Anomaly Detection Model Report.
	var model *detect.Model
	if strings.TrimSpace(*modelPath) != "" {
		model, err = server.LoadModel(*modelPath)
		if err != nil {
			slog.Warn("anomaly model unavailable; continuing without the isolation forest layer",
				"path", *modelPath, "err", err)
		} else {
			slog.Info("anomaly model loaded", "path", *modelPath, "forests", server.ModelSummary(model))
		}
	} else {
		slog.Warn("no -model supplied; running without the isolation forest layer")
	}

	alertBus := server.NewAlertBus(server.DefaultAlertBuffer)
	detection := server.NewDetectionPipeline(model, server.MultiSink{server.LogSink{}, alertBus}, *detectQueue)
	go detection.Run(ctx)
	go detection.WatchLiveness(ctx, store, *aliveWindow, 0)

	// Drain the bus so publication never blocks. Operator-facing consumers
	// attach here; until D3.1 binds alerts to a testbed slice, draining keeps
	// the channel from filling and the priority classification observable.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-alertBus.Alerts():
			}
		}
	}()

	// HTTP handler enforces validation + optional auth before updating the store.
	// Context drives background sweepers (replay cache + rate limiter).
	handler, err := server.NewHandler(server.HandlerConfig{
		Store:           store,
		Logger:          csvLogger,
		ClientPorts:     portLogger,
		APIKey:          *apiKey,
		KeyStorePath:    *keyStorePath,
		RateLimitRPS:    *rateLimitRPS,
		RateLimitBurst:  *rateLimitBurst,
		Context:         ctx,
		IdleBucketAfter: *rateLimitIdle,
		AccessLog:       *accessLog,
		Detection:       detection,
	})
	if err != nil {
		slog.Error("handler init", "err", err)
		os.Exit(1)
	}

	// Configure timeouts for the telemetry API.
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Start the HTTP server and dashboard in separate goroutines. A fatal listen
	// error is propagated to main so shutdown runs through the same path.
	var dashboardLogs *server.DashboardLogs
	var logOutput io.Writer = os.Stderr
	if !*healthcheck && server.IsTerminal(os.Stdout) && server.IsTerminal(os.Stderr) {
		dashboardLogs = &server.DashboardLogs{}
		logOutput = dashboardLogs
	}
	configureLogger(*logFormat, logOutput)

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", addr, "tls", useTLS)
		var err error
		if useTLS {
			err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	if *evictAfter > 0 {
		go func() {
			t := time.NewTicker(*evictAfter / 2)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					// The store notifies detection for both idle and capacity eviction.
					store.Evict(*evictAfter)
				}
			}
		}()
	}

	dashboardCtx, stopDashboard := context.WithCancel(ctx)
	dashboardDone := make(chan struct{})
	go func() {
		defer close(dashboardDone)
		server.RenderDashboard(dashboardCtx, store, addr, *aliveWindow, dashboardLogs)
	}()

	// Block until a signal is received or the listener fails, then shut down.
	select {
	case <-ctx.Done():
	case err := <-serverErr:
		slog.Error("listen error", "err", err)
	}
	stopDashboard()
	<-dashboardDone
	configureLogger(*logFormat, os.Stderr)
	if dashboardLogs != nil {
		for _, line := range dashboardLogs.Snapshot() {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	// Report detection coverage at shutdown. A non-zero dropped count means the
	// detector fell behind ingest and some readings went unscored, which is an
	// operational signal rather than a failure.
	scored, alerts, dropped := detection.Stats()
	slog.Info("detection summary",
		"scored", scored, "alerts", alerts, "dropped", dropped,
		"alerts_undelivered", alertBus.Dropped())
	fmt.Println("\nshutting down")
}

// probeHealth performs a loopback GET against /health and returns a process
// exit code. Certificate verification is skipped because the target is this
// process on the loopback interface, where the certificate's hostname carries
// no information the probe could act on.
func probeHealth(port string, useTLS bool) int {
	scheme := "http"
	client := &http.Client{Timeout: 3 * time.Second}
	if useTLS {
		scheme = "https"
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback self-probe
		}
	}
	url := scheme + "://127.0.0.1:" + strings.TrimPrefix(port, ":") + "/health"
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}

// configureLogger installs a structured logger on slog.Default writing to
// the selected destination; interactive logs are rendered inside the dashboard.
func configureLogger(format string, output io.Writer) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		h = slog.NewJSONHandler(output, opts)
	default:
		h = slog.NewTextHandler(output, opts)
	}
	slog.SetDefault(slog.New(h))
}

// Keep environment credentials out of flag defaults: flag help prints defaults.
func configuredAPIKey(value string) string {
	if value != "" {
		return value
	}
	return os.Getenv("PQHEAL_API_KEY")
}
