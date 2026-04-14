package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

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
		return zero, fmt.Errorf("HTTP %d: %s", status, string(respBody))
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
		return "", fmt.Errorf("HTTP %d: %s", status, string(respBody))
	}
	return string(respBody), nil
}

// httpNoBody performs an HTTP request and discards the body.
func httpNoBody(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any) error {
	_, status, err := httpDo(ctx, client, method, url, headers, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}
