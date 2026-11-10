package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIError captures a structured non-2xx response from the server. The body is
// expected to match {"error":{"code":"...","message":"...","request_id":"..."}}
// but Code/Message fall back to empty strings when the body doesn't parse.
type APIError struct {
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("server %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("server %d: %s", e.Status, e.Body)
}

func PostJSON(client *http.Client, url string, payload any, apiKey string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		raw := readLimited(resp.Body, 512)
		apiErr := &APIError{Status: resp.StatusCode, Body: raw}
		var parsed struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			apiErr.Code = parsed.Error.Code
			apiErr.Message = parsed.Error.Message
		}
		return apiErr
	}

	return nil
}

// readLimited reads a small error body to include in HTTP failures.
func readLimited(r io.Reader, limit int64) string {
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
