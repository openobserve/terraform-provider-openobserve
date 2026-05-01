package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
			Timeout: 30 * time.Second,
		},
	}
}

// apiError is returned when the API responds with a non-2xx status.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("OpenObserve API error (HTTP %d): %s", e.StatusCode, e.Body)
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
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &apiError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	return respBody, resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// Stream API
// ---------------------------------------------------------------------------

// StreamSettingsAPI is the wire format for stream settings.
type StreamSettingsAPI struct {
	DataRetention      int                  `json:"data_retention,omitempty"`
	PartitionKeys      []StreamPartitionKey `json:"partition_keys,omitempty"`
	FullTextSearchKeys []string             `json:"full_text_search_keys,omitempty"`
	IndexFields        []string             `json:"index_fields,omitempty"`
	BloomFilterFields  []string             `json:"bloom_filter_fields,omitempty"`
}

// StreamPartitionKey represents a single partition key configuration.
type StreamPartitionKey struct {
	Field string   `json:"field"`
	Types []string `json:"types"`
}

// StreamSchemaResponse is returned by GET /api/{org}/streams/{name}/schema.
type StreamSchemaResponse struct {
	Name        string            `json:"name"`
	StorageType string            `json:"storage_type"`
	StreamType  string            `json:"stream_type"`
	Settings    StreamSettingsAPI `json:"settings"`
}

func (c *Client) GetStreamSchema(ctx context.Context, orgID, streamType, name string) (*StreamSchemaResponse, error) {
	path := fmt.Sprintf("/api/%s/streams/%s/schema?type=%s", orgID, name, streamType)
	body, statusCode, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil // caller interprets nil as "does not exist"
		}
		return nil, err
	}
	if statusCode == http.StatusNotFound {
		return nil, nil
	}

	var result StreamSchemaResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing stream schema response: %w", err)
	}
	return &result, nil
}

func (c *Client) UpdateStreamSettings(ctx context.Context, orgID, streamType, name string, settings StreamSettingsAPI) error {
	path := fmt.Sprintf("/api/%s/streams/%s/settings?type=%s", orgID, name, streamType)
	_, _, err := c.doRequest(ctx, http.MethodPut, path, settings)
	return err
}

func (c *Client) DeleteStream(ctx context.Context, orgID, streamType, name string) error {
	path := fmt.Sprintf("/api/%s/streams/%s?type=%s", orgID, name, streamType)
	_, statusCode, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil // already gone
		}
		return err
	}
	_ = statusCode
	return nil
}

// ---------------------------------------------------------------------------
// Dashboard API
// ---------------------------------------------------------------------------

// DashboardAPI is the wire format for create/update dashboard requests.
type DashboardAPI struct {
	DashboardID string `json:"dashboardId,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Panels      any    `json:"panels,omitempty"`
	Variables   any    `json:"variables,omitempty"`
}

// DashboardResponse is returned by create/get dashboard endpoints.
type DashboardResponse struct {
	DashboardID string `json:"dashboardId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Role        string `json:"role"`
	Owner       string `json:"owner"`
	Version     int    `json:"version"`
}

func (c *Client) CreateDashboard(ctx context.Context, orgID string, req DashboardAPI) (*DashboardResponse, error) {
	path := fmt.Sprintf("/api/%s/dashboards", orgID)
	body, _, err := c.doRequest(ctx, http.MethodPost, path, req)
	if err != nil {
		return nil, err
	}
	var result DashboardResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing dashboard create response: %w", err)
	}
	return &result, nil
}

func (c *Client) GetDashboard(ctx context.Context, orgID, dashboardID string) (*DashboardResponse, error) {
	path := fmt.Sprintf("/api/%s/dashboards/%s", orgID, dashboardID)
	body, statusCode, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if statusCode == http.StatusNotFound {
		return nil, nil
	}
	var result DashboardResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing dashboard get response: %w", err)
	}
	return &result, nil
}

func (c *Client) UpdateDashboard(ctx context.Context, orgID, dashboardID string, req DashboardAPI) (*DashboardResponse, error) {
	path := fmt.Sprintf("/api/%s/dashboards/%s", orgID, dashboardID)
	body, _, err := c.doRequest(ctx, http.MethodPut, path, req)
	if err != nil {
		return nil, err
	}
	var result DashboardResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing dashboard update response: %w", err)
	}
	return &result, nil
}

func (c *Client) DeleteDashboard(ctx context.Context, orgID, dashboardID string) error {
	path := fmt.Sprintf("/api/%s/dashboards/%s", orgID, dashboardID)
	_, _, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// User API
// ---------------------------------------------------------------------------

// UserAPI is the wire format for create/update user requests.
type UserAPI struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Password  string `json:"password,omitempty"`
	Role      string `json:"role"`
}

// UserResponse is returned by create/get user endpoints.
type UserResponse struct {
	Email      string `json:"email"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	Role       string `json:"role"`
	IsExternal bool   `json:"is_external"`
}

// UsersResponse wraps the users list response.
type UsersResponse struct {
	Data []UserResponse `json:"data"`
}

func (c *Client) CreateUser(ctx context.Context, orgID string, req UserAPI) (*UserResponse, error) {
	path := fmt.Sprintf("/api/%s/users", orgID)
	body, _, err := c.doRequest(ctx, http.MethodPost, path, req)
	if err != nil {
		return nil, err
	}
	var result UserResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing user create response: %w", err)
	}
	return &result, nil
}

func (c *Client) GetUser(ctx context.Context, orgID, email string) (*UserResponse, error) {
	path := fmt.Sprintf("/api/%s/users", orgID)
	body, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var result UsersResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}
	for _, u := range result.Data {
		if u.Email == email {
			return &u, nil
		}
	}
	return nil, nil // not found
}

func (c *Client) UpdateUser(ctx context.Context, orgID, email string, req UserAPI) (*UserResponse, error) {
	path := fmt.Sprintf("/api/%s/users/%s", orgID, email)
	body, _, err := c.doRequest(ctx, http.MethodPut, path, req)
	if err != nil {
		return nil, err
	}
	var result UserResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing user update response: %w", err)
	}
	return &result, nil
}

func (c *Client) DeleteUser(ctx context.Context, orgID, email string) error {
	path := fmt.Sprintf("/api/%s/users/%s", orgID, email)
	_, _, err := c.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Organization API
// ---------------------------------------------------------------------------

// OrganizationAPI represents a single organization from the API.
type OrganizationAPI struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Type       int    `json:"org_type"`
	UserEmail  string `json:"user_email"`
}

// OrganizationsResponse wraps the org list endpoint.
type OrganizationsResponse struct {
	Data []OrganizationAPI `json:"data"`
}

func (c *Client) ListOrganizations(ctx context.Context) ([]OrganizationAPI, error) {
	body, _, err := c.doRequest(ctx, http.MethodGet, "/api/organizations", nil)
	if err != nil {
		return nil, err
	}
	var result OrganizationsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing organizations response: %w", err)
	}
	return result.Data, nil
}

func (c *Client) GetOrganization(ctx context.Context, identifier string) (*OrganizationAPI, error) {
	orgs, err := c.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orgs {
		if o.Identifier == identifier {
			return &o, nil
		}
	}
	return nil, nil
}
