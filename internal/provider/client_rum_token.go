package provider

import (
	"context"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// RUM tokens
// ---------------------------------------------------------------------------
//
// The browser RUM intake (/rum/v1/{org}/...) accepts only a user's RUM token,
// not an ingestion token. Every org membership gets one, and the server hands
// it only to the signed-in user, so the provider can read the token of the
// user it authenticates as and nobody else's. Like ingestion tokens, the
// response wraps the payload in `data`.

// RumTokenAPI is the wire format of GET /api/{org}/rumtoken.
type RumTokenAPI struct {
	User     string  `json:"user"`
	RumToken *string `json:"rum_token"`
}

type rumTokenResponse struct {
	Data RumTokenAPI `json:"data"`
}

// GetRumToken returns the signed-in user's RUM token for an organization.
func (c *Client) GetRumToken(ctx context.Context, orgID string) (*RumTokenAPI, error) {
	path := fmt.Sprintf("/api/%s/rumtoken", pathEscape(orgID))
	var out rumTokenResponse
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}
