package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// OrgUserAPI is the nested user object attached to an organization listing.
type OrgUserAPI struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
}

// OrganizationAPI is one entry of GET /api/organizations.
type OrganizationAPI struct {
	ID              int64      `json:"id"`
	Identifier      string     `json:"identifier"`
	Name            string     `json:"name"`
	UserEmail       string     `json:"user_email"`
	IngestThreshold int64      `json:"ingest_threshold"`
	SearchThreshold int64      `json:"search_threshold"`
	OrgType         string     `json:"type"`
	UserObj         OrgUserAPI `json:"UserObj"`
	Plan            int        `json:"plan"`
}

// OrganizationListAPI wraps the organizations list response.
type OrganizationListAPI struct {
	Data []OrganizationAPI `json:"data"`
}

// CreateOrgAPI is the body of POST /api/organizations.
type CreateOrgAPI struct {
	Identifier     string  `json:"identifier,omitempty"`
	Name           string  `json:"name"`
	OrgType        string  `json:"org_type,omitempty"`
	ServiceAccount *string `json:"service_account,omitempty"`
}

// CreateOrgResponse is returned by POST /api/organizations.
type CreateOrgResponse struct {
	Identifier     string                        `json:"identifier"`
	Name           string                        `json:"name"`
	OrgType        string                        `json:"org_type"`
	ServiceAccount *ServiceAccountCreateResponse `json:"service_account,omitempty"`
}

// ListOrganizations returns every organization visible to the caller.
func (c *Client) ListOrganizations(ctx context.Context) ([]OrganizationAPI, error) {
	var out OrganizationListAPI
	if err := c.doJSON(ctx, http.MethodGet, "/api/organizations", nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetOrganization resolves an organization identifier. It returns (nil, nil)
// when no such organization exists.
func (c *Client) GetOrganization(ctx context.Context, identifier string) (*OrganizationAPI, error) {
	orgs, err := c.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		if orgs[i].Identifier == identifier {
			return &orgs[i], nil
		}
	}
	return nil, nil
}

// CreateOrganization creates a new organization.
func (c *Client) CreateOrganization(ctx context.Context, req CreateOrgAPI) (*CreateOrgResponse, error) {
	var out CreateOrgResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/organizations", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenameOrganization changes an organization's display name. The identifier is
// immutable.
func (c *Client) RenameOrganization(ctx context.Context, identifier, newName string) error {
	path := fmt.Sprintf("/api/%s/rename", pathEscape(identifier))
	return c.do(ctx, http.MethodPut, path, map[string]string{"new_name": newName})
}

// equalFold compares two identifiers case-insensitively. Email addresses and
// organization identifiers are normalised to lowercase by the server.
func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}
