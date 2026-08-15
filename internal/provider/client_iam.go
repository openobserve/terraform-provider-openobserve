package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// CreateUserAPI is the body of POST /api/{org}/users.
//
// `role` and `custom_role` are flattened into the request by the server, so
// they sit at the top level rather than inside a nested object.
type CreateUserAPI struct {
	Email      string   `json:"email"`
	FirstName  string   `json:"first_name"`
	LastName   string   `json:"last_name"`
	Password   string   `json:"password"`
	Role       string   `json:"role"`
	CustomRole []string `json:"custom_role,omitempty"`
	IsExternal bool     `json:"is_external,omitempty"`
}

// UpdateUserAPI is the body of PUT /api/{org}/users/{email}.
type UpdateUserAPI struct {
	ChangePassword bool     `json:"change_password"`
	FirstName      *string  `json:"first_name,omitempty"`
	LastName       *string  `json:"last_name,omitempty"`
	OldPassword    *string  `json:"old_password,omitempty"`
	NewPassword    *string  `json:"new_password,omitempty"`
	Role           *string  `json:"role,omitempty"`
	CustomRole     []string `json:"custom_role,omitempty"`
}

// AddUserToOrgAPI is the body of POST /api/{org}/users/{email}.
type AddUserToOrgAPI struct {
	Role       string   `json:"role"`
	CustomRole []string `json:"custom_role,omitempty"`
}

// UserAPI is one entry of the users list response.
type UserAPI struct {
	Email       string           `json:"email"`
	FirstName   string           `json:"first_name"`
	LastName    string           `json:"last_name"`
	Role        string           `json:"role"`
	IsExternal  bool             `json:"is_external"`
	CreatedAt   int64            `json:"created_at"`
	Token       string           `json:"token,omitempty"`
	IsSystem    bool             `json:"is_system,omitempty"`
	Description string           `json:"description,omitempty"`
	Orgs        []OrgRoleMapping `json:"orgs,omitempty"`
}

// OrgRoleMapping ties a user to a role within one organization.
type OrgRoleMapping struct {
	OrgID   string `json:"org_id"`
	OrgName string `json:"org_name"`
	Role    string `json:"role"`
}

// UserListAPI wraps the users list response.
type UserListAPI struct {
	Data []UserAPI `json:"data"`
}

// ServiceAccountCreateResponse is returned when creating a service account. It
// is the only place the plaintext API token is ever exposed.
type ServiceAccountCreateResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Token   string `json:"token"`
	User    string `json:"user"`
}

// APITokenResponse is returned when rotating a service account token.
type APITokenResponse struct {
	Token string `json:"token"`
	User  string `json:"user"`
}

// ServiceAccountAPI is the body of the service account create/update endpoints.
type ServiceAccountAPI struct {
	Email     string `json:"email,omitempty"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// CreateUser adds a new user to an organization.
func (c *Client) CreateUser(ctx context.Context, orgID string, req CreateUserAPI) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/users", pathEscape(orgID)), req)
}

// ListUsers returns the users of an organization.
func (c *Client) ListUsers(ctx context.Context, orgID string) ([]UserAPI, error) {
	var out UserListAPI
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/users", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetUser looks a user up by email. It returns (nil, nil) when absent.
func (c *Client) GetUser(ctx context.Context, orgID, email string) (*UserAPI, error) {
	users, err := c.ListUsers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range users {
		if equalFold(users[i].Email, email) {
			return &users[i], nil
		}
	}
	return nil, nil
}

// UpdateUser changes an existing user's profile, role, or password.
func (c *Client) UpdateUser(ctx context.Context, orgID, email string, req UpdateUserAPI) error {
	path := fmt.Sprintf("/api/%s/users/%s", pathEscape(orgID), pathEscape(email))
	return c.do(ctx, http.MethodPut, path, req)
}

// AddUserToOrg grants an existing user a role in another organization.
//
// A user who is already a member answers with HTTP 409; callers that are
// reconciling toward that state should treat it as success.
func (c *Client) AddUserToOrg(ctx context.Context, orgID, email string, req AddUserToOrgAPI) error {
	path := fmt.Sprintf("/api/%s/users/%s", pathEscape(orgID), pathEscape(email))
	return c.do(ctx, http.MethodPost, path, req)
}

// DeleteUser removes a user from an organization.
func (c *Client) DeleteUser(ctx context.Context, orgID, email string) error {
	return c.deleteIgnoreMissing(ctx, fmt.Sprintf("/api/%s/users/%s", pathEscape(orgID), pathEscape(email)))
}

// UserRoleOption is one entry of the built-in role list.
type UserRoleOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ListUserRoles returns the built-in roles the server accepts.
//
// The endpoint answers with `[{"label":"Admin","value":"admin"}]` on current
// builds and with a bare `["admin"]` on older ones, so both are decoded.
func (c *Client) ListUserRoles(ctx context.Context, orgID string) ([]UserRoleOption, error) {
	path := fmt.Sprintf("/api/%s/users/roles", pathEscape(orgID))
	raw, _, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var options []UserRoleOption
	if err := json.Unmarshal(raw, &options); err == nil {
		return options, nil
	}

	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("parsing user roles response: %w (body: %s)", err, truncate(string(raw), 256))
	}
	options = make([]UserRoleOption, 0, len(names))
	for _, n := range names {
		options = append(options, UserRoleOption{Label: n, Value: n})
	}
	return options, nil
}

// ---------------------------------------------------------------------------
// Service accounts
// ---------------------------------------------------------------------------

// CreateServiceAccount creates a service account and returns its API token.
func (c *Client) CreateServiceAccount(ctx context.Context, orgID string, req ServiceAccountAPI) (*ServiceAccountCreateResponse, error) {
	var out ServiceAccountCreateResponse
	path := fmt.Sprintf("/api/%s/service_accounts", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListServiceAccounts returns the service accounts of an organization.
func (c *Client) ListServiceAccounts(ctx context.Context, orgID string) ([]UserAPI, error) {
	var out UserListAPI
	path := fmt.Sprintf("/api/%s/service_accounts", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// GetServiceAccount looks a service account up by email.
func (c *Client) GetServiceAccount(ctx context.Context, orgID, email string) (*UserAPI, error) {
	accounts, err := c.ListServiceAccounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if equalFold(accounts[i].Email, email) {
			return &accounts[i], nil
		}
	}
	return nil, nil
}

// UpdateServiceAccount changes a service account's display name.
func (c *Client) UpdateServiceAccount(ctx context.Context, orgID, email string, req ServiceAccountAPI) error {
	path := fmt.Sprintf("/api/%s/service_accounts/%s", pathEscape(orgID), pathEscape(email))
	return c.do(ctx, http.MethodPut, path, req)
}

// RotateServiceAccountToken issues a fresh API token, invalidating the old one.
func (c *Client) RotateServiceAccountToken(ctx context.Context, orgID, email string) (*APITokenResponse, error) {
	path := fmt.Sprintf("/api/%s/service_accounts/%s?rotateToken=true", pathEscape(orgID), pathEscape(email))
	var out APITokenResponse
	if err := c.doJSON(ctx, http.MethodPut, path, ServiceAccountAPI{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteServiceAccount removes a service account and revokes its token.
func (c *Client) DeleteServiceAccount(ctx context.Context, orgID, email string) error {
	path := fmt.Sprintf("/api/%s/service_accounts/%s", pathEscape(orgID), pathEscape(email))
	return c.deleteIgnoreMissing(ctx, path)
}

// ---------------------------------------------------------------------------
// Roles and permissions
// ---------------------------------------------------------------------------

// CreateRoleAPI is the body of POST /api/{org}/roles.
type CreateRoleAPI struct {
	Role string `json:"role"`
}

// EntityAuthorization binds a permission to an object such as `stream:mystream`
// or a whole resource type such as `stream`.
type EntityAuthorization struct {
	Object     string `json:"object"`
	Permission string `json:"permission"`
}

// UpdateRoleAPI is the body of PUT /api/{org}/roles/{role}. Permissions and
// members are expressed as deltas against the current state.
type UpdateRoleAPI struct {
	Add         []EntityAuthorization `json:"add"`
	Remove      []EntityAuthorization `json:"remove"`
	AddUsers    []string              `json:"add_users,omitempty"`
	RemoveUsers []string              `json:"remove_users,omitempty"`
}

// ResourceAPI describes one permissible resource type.
type ResourceAPI struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Order       int    `json:"order"`
	Parent      string `json:"parent"`
	Visible     bool   `json:"visible"`
	HasEntities bool   `json:"has_entities"`
}

// CreateRole creates a custom role.
func (c *Client) CreateRole(ctx context.Context, orgID, role string) error {
	path := fmt.Sprintf("/api/%s/roles", pathEscape(orgID))
	return c.do(ctx, http.MethodPost, path, CreateRoleAPI{Role: role})
}

// ListRoles returns the custom role names in an organization.
func (c *Client) ListRoles(ctx context.Context, orgID string) ([]string, error) {
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/roles", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// RoleExists reports whether a custom role is present.
func (c *Client) RoleExists(ctx context.Context, orgID, role string) (bool, error) {
	roles, err := c.ListRoles(ctx, orgID)
	if err != nil {
		return false, err
	}
	for _, r := range roles {
		if r == role {
			return true, nil
		}
	}
	return false, nil
}

// UpdateRole applies a permission and membership delta to a role.
func (c *Client) UpdateRole(ctx context.Context, orgID, role string, req UpdateRoleAPI) error {
	if req.Add == nil {
		req.Add = []EntityAuthorization{}
	}
	if req.Remove == nil {
		req.Remove = []EntityAuthorization{}
	}
	path := fmt.Sprintf("/api/%s/roles/%s", pathEscape(orgID), pathEscape(role))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteRole removes a custom role.
func (c *Client) DeleteRole(ctx context.Context, orgID, role string) error {
	return c.deleteIgnoreMissing(ctx, fmt.Sprintf("/api/%s/roles/%s", pathEscape(orgID), pathEscape(role)))
}

// GetRolePermissions returns every permission granted to a role.
func (c *Client) GetRolePermissions(ctx context.Context, orgID, role string) ([]EntityAuthorization, error) {
	path := fmt.Sprintf("/api/%s/roles/%s/permissions", pathEscape(orgID), pathEscape(role))
	var out []EntityAuthorization
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRoleUsers returns the users directly assigned to a role.
func (c *Client) GetRoleUsers(ctx context.Context, orgID, role string) ([]string, error) {
	path := fmt.Sprintf("/api/%s/roles/%s/users", pathEscape(orgID), pathEscape(role))
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// ListResources returns the resource types that can appear in a permission.
func (c *Client) ListResources(ctx context.Context, orgID string) ([]ResourceAPI, error) {
	var out []ResourceAPI
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/resources", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetUserRoles returns the custom roles assigned to a user.
func (c *Client) GetUserRoles(ctx context.Context, orgID, email string) ([]string, error) {
	path := fmt.Sprintf("/api/%s/users/%s/roles", pathEscape(orgID), pathEscape(email))
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// GetUserGroups returns the groups a user belongs to.
func (c *Client) GetUserGroups(ctx context.Context, orgID, email string) ([]string, error) {
	path := fmt.Sprintf("/api/%s/users/%s/groups", pathEscape(orgID), pathEscape(email))
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

// GroupAPI is the body of POST /api/{org}/groups and the shape returned by the
// group details endpoint.
type GroupAPI struct {
	Name  string   `json:"name"`
	Users []string `json:"users"`
	Roles []string `json:"roles"`
}

// UpdateGroupAPI is the body of PUT /api/{org}/groups/{name}. Membership is
// expressed as a delta against the current state.
type UpdateGroupAPI struct {
	AddUsers    []string `json:"add_users,omitempty"`
	RemoveUsers []string `json:"remove_users,omitempty"`
	AddRoles    []string `json:"add_roles,omitempty"`
	RemoveRoles []string `json:"remove_roles,omitempty"`
}

// CreateGroup creates an empty user group. Members and roles are attached
// afterwards through UpdateGroup.
//
// The group is deliberately created empty. OpenObserve only records the
// group-to-organization link when the create request carries no users; seeding
// members instead writes only the membership links, and the group then never
// appears in the group listing (nor in the UI, which reads the same endpoint).
// Creating empty and then adding members avoids that, and repairs a group that
// was already created the other way.
func (c *Client) CreateGroup(ctx context.Context, orgID, name string) error {
	body := GroupAPI{Name: name, Users: []string{}, Roles: []string{}}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/groups", pathEscape(orgID)), body)
}

// ListGroups returns the group names in an organization.
func (c *Client) ListGroups(ctx context.Context, orgID string) ([]string, error) {
	var out []string
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/groups", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// GetGroup returns a group's members and roles. It returns (nil, nil) when the
// group does not exist.
func (c *Client) GetGroup(ctx context.Context, orgID, name string) (*GroupAPI, error) {
	// The details endpoint answers 200 with an empty payload for unknown
	// groups, so existence is settled against the list endpoint first.
	groups, err := c.ListGroups(ctx, orgID)
	if err != nil {
		return nil, err
	}
	present := false
	for _, g := range groups {
		if g == name {
			present = true
			break
		}
	}
	if !present {
		return nil, nil
	}

	path := fmt.Sprintf("/api/%s/groups/%s", pathEscape(orgID), pathEscape(name))
	var out GroupAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if out.Name == "" {
		out.Name = name
	}
	sort.Strings(out.Users)
	sort.Strings(out.Roles)
	return &out, nil
}

// UpdateGroup applies a membership delta to a group.
func (c *Client) UpdateGroup(ctx context.Context, orgID, name string, req UpdateGroupAPI) error {
	path := fmt.Sprintf("/api/%s/groups/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteGroup removes a group.
func (c *Client) DeleteGroup(ctx context.Context, orgID, name string) error {
	return c.deleteIgnoreMissing(ctx, fmt.Sprintf("/api/%s/groups/%s", pathEscape(orgID), pathEscape(name)))
}
