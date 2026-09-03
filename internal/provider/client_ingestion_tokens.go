package provider

import (
	"context"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// Ingestion tokens
// ---------------------------------------------------------------------------
//
// An ingestion token authenticates data going *into* an organization, as
// opposed to a service account, which authenticates calls to the management
// API. Collectors and SDKs carry one.
//
// Two things about this API shape the resource:
//
// There is no delete endpoint. Tokens can be created, listed and
// enabled/disabled, and that is all, so a Terraform destroy disables the token
// rather than removing it. The same is true of organizations.
//
// Responses wrap the payload in `data`, unlike most of the API.

// IngestionTokenAPI is the wire format of an ingestion token.
type IngestionTokenAPI struct {
	Name        string `json:"name"`
	Token       string `json:"token"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default"`
	Enabled     bool   `json:"enabled"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`
}

// ingestionTokenResponse wraps a single token.
type ingestionTokenResponse struct {
	Data IngestionTokenAPI `json:"data"`
}

// ingestionTokenListResponse wraps a token listing.
type ingestionTokenListResponse struct {
	Data []IngestionTokenAPI `json:"data"`
}

// CreateIngestionTokenAPI is the create body.
type CreateIngestionTokenAPI struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// CreateIngestionToken issues a new ingestion token and returns it, including
// the secret. The secret is only available here and in the listing.
func (c *Client) CreateIngestionToken(ctx context.Context, orgID string, req CreateIngestionTokenAPI) (*IngestionTokenAPI, error) {
	path := fmt.Sprintf("/api/%s/ingestion-tokens", pathEscape(orgID))
	var out ingestionTokenResponse
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// ListIngestionTokens returns every ingestion token in an organization.
func (c *Client) ListIngestionTokens(ctx context.Context, orgID string) ([]IngestionTokenAPI, error) {
	path := fmt.Sprintf("/api/%s/ingestion-tokens", pathEscape(orgID))
	var out ingestionTokenListResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetIngestionToken reads one token by name. It returns (nil, nil) when absent.
//
// There is no single-token endpoint, so this reads the listing.
func (c *Client) GetIngestionToken(ctx context.Context, orgID, name string) (*IngestionTokenAPI, error) {
	tokens, err := c.ListIngestionTokens(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range tokens {
		if tokens[i].Name == name {
			return &tokens[i], nil
		}
	}
	return nil, nil
}

// SetIngestionTokenEnabled enables or disables a token. Disabling is the
// closest thing to deletion the API offers.
func (c *Client) SetIngestionTokenEnabled(ctx context.Context, orgID, name string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/ingestion-tokens/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPatch, path, map[string]bool{"enabled": enabled})
}
