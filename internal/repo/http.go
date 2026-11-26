package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HTTPError is a response with a non-2xx status: the request reached the
// provider and was answered. Any other error means the outcome is unknown.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := e.Body
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	if body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, body)
}

// httpDo performs an HTTP request and returns the response body.
func httpDo(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

// httpJSON performs an HTTP request and unmarshals the response.
func httpJSON[T any](ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any) (T, error) {
	var zero T
	respBody, status, err := httpDo(ctx, client, method, url, headers, body)
	if err != nil {
		return zero, err
	}
	if status < 200 || status >= 300 {
		return zero, &HTTPError{Status: status, Body: string(respBody)}
	}
	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return zero, fmt.Errorf("unmarshal response: %w", err)
	}
	return result, nil
}

// httpText performs an HTTP request and returns the body as text.
func httpText(ctx context.Context, client *http.Client, method, url string, headers map[string]string) (string, error) {
	respBody, status, err := httpDo(ctx, client, method, url, headers, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", &HTTPError{Status: status, Body: string(respBody)}
	}
	return string(respBody), nil
}

// httpNoBody performs an HTTP request and discards the body.
func httpNoBody(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any) error {
	respBody, status, err := httpDo(ctx, client, method, url, headers, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &HTTPError{Status: status, Body: string(respBody)}
	}
	return nil
}
