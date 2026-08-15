package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the OpenObserve API client used by all resources and data sources.
type Client struct {
	baseURL    string
	username   string
	password   string
	orgID      string
	httpClient *http.Client
}

func newClient(baseURL, username, password, orgID string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		orgID:    orgID,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// DefaultOrgID returns the provider-level organization identifier.
func (c *Client) DefaultOrgID() string {
	return c.orgID
}

// apiError is returned when the API responds with a non-2xx status.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("OpenObserve API error (HTTP %d): %s", e.StatusCode, e.Body)
}

// isNotFound reports whether err is an API error carrying HTTP 404.
func isNotFound(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// isAlreadyExists reports whether err is a 4xx complaining the object exists.
// OpenObserve returns HTTP 400 with a "already exists" message rather than 409.
func isAlreadyExists(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusConflict {
			return true
		}
		return apiErr.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(apiErr.Body), "already exists")
	}
	return false
}

// isNotSupported reports whether err is the 403 OpenObserve returns for an
// endpoint that only exists in the Enterprise edition.
func isNotSupported(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusForbidden &&
			strings.Contains(apiErr.Body, "Not Supported")
	}
	return false
}

// enterpriseFeatureError turns the bare "Not Supported" 403 into a diagnostic
// that says what is actually missing.
func enterpriseFeatureError(feature string, err error) (string, string) {
	if isNotSupported(err) {
		return feature + " requires OpenObserve Enterprise",
			"The server answered 403 Not Supported. " + feature + " is an Enterprise feature and also needs " +
				"OpenFGA enabled (ZO_OPENFGA_ENABLED=true). This provider resource cannot be used against an " +
				"open-source deployment."
	}
	return "Error managing " + feature, err.Error()
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("executing request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close error is not actionable

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	return respBody, resp.StatusCode, nil
}

// do issues a request and discards the response body.
func (c *Client) do(ctx context.Context, method, path string, body any) error {
	_, _, err := c.doRequest(ctx, method, path, body)
	return err
}

// doJSON issues a request and decodes a JSON response into out.
func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	respBody, _, err := c.doRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("parsing response from %s %s: %w (body: %s)", method, path, err, truncate(string(respBody), 512))
	}
	return nil
}

// doJSONOptional behaves like doJSON but maps HTTP 404 to (false, nil).
func (c *Client) doJSONOptional(ctx context.Context, method, path string, body, out any) (bool, error) {
	if err := c.doJSON(ctx, method, path, body, out); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deleteIgnoreMissing issues a DELETE and swallows a 404 so deletes are idempotent.
func (c *Client) deleteIgnoreMissing(ctx context.Context, path string) error {
	if err := c.do(ctx, http.MethodDelete, path, nil); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// pathEscape escapes a single URL path segment.
func pathEscape(s string) string {
	return url.PathEscape(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// MessageResponse is the generic {code, message, id, name} envelope OpenObserve
// returns from many write endpoints.
type MessageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
}
