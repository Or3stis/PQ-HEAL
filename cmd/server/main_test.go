package main

import (
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestConfiguredAPIKey(t *testing.T) {
	t.Setenv("PQHEAL_API_KEY", "environment-secret")
	if got := configuredAPIKey(""); got != "environment-secret" {
		t.Fatal("environment credential not loaded")
	}
	if got := configuredAPIKey("explicit-secret"); got != "explicit-secret" {
		t.Fatal("explicit flag must override environment")
	}
	t.Setenv("PQHEAL_API_KEY", "")
	if configuredAPIKey("") != "" {
		t.Fatal("missing credentials must stay empty for startup validation")
	}
}

// Run the real entrypoint in an isolated process with no API key argument.
func TestCredentialProcess(t *testing.T) {
	mode := os.Getenv("PQHEAL_CREDENTIAL_TEST_PROCESS")
	if mode == "" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("pq-heal-server", flag.ExitOnError)
	os.Args = []string{"pq-heal-server"}
	if mode == "help" {
		os.Args = append(os.Args, "-h")
	} else {
		os.Args = append(os.Args, "-bind=127.0.0.1", "-port="+mode, "-log=", "-client-port-log=", "-key-store=")
	}
	main()
}

func TestEnvironmentCredentialAuthenticatesWithoutArgument(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PQHEAL_API_KEY", "environment-only-test-secret")
	t.Setenv("PQHEAL_CREDENTIAL_TEST_PROCESS", port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCredentialProcess$")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	url := "http://127.0.0.1:" + port
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, err := client.Get(url + "/health")
		if err == nil {
			resp.Body.Close()
			ready = resp.StatusCode == 200
			if ready {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("server did not start with an environment-only credential")
	}
	for _, authorized := range []bool{false, true} {
		req, _ := http.NewRequest(http.MethodGet, url+"/clients", nil)
		want := http.StatusUnauthorized
		if authorized {
			req.Header.Set("Authorization", "Bearer environment-only-test-secret")
			want = http.StatusOK
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("authorized=%v: status=%d want=%d", authorized, resp.StatusCode, want)
		}
	}
}

func TestHelpDoesNotExposeEnvironmentCredential(t *testing.T) {
	const secret = "help-must-not-print-this-secret"
	t.Setenv("PQHEAL_API_KEY", secret)
	t.Setenv("PQHEAL_CREDENTIAL_TEST_PROCESS", "help")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCredentialProcess$")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help failed: %v", err)
	}
	if strings.Contains(string(out), secret) {
		t.Fatal("flag help exposed the environment credential")
	}
}
