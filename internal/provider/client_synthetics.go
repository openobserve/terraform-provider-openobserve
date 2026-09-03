package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Synthetics
// ---------------------------------------------------------------------------
//
// A synthetic check probes a target from one or more locations on a schedule,
// and notifies alert destinations when it fails. Five kinds: HTTP, TCP, TLS
// certificate, SSH, and a scripted browser journey.
//
// The whole family is gated behind ZO_SYNTHETICS_ENABLED. When it is off the
// routes are not registered at all, so every path answers 404 rather than a
// diagnostic saying the feature is disabled. isSyntheticsDisabled turns that
// into something a user can act on.
//
// It follows the alert pattern closely: the destination folder comes from a
// `?folder=` query parameter and any folder in the body is ignored, enabling is
// its own endpoint, and moving between folders is a separate PATCH.

// SyntheticTypes are the accepted check kinds.
var SyntheticTypes = []string{"http", "tcp", "tls", "ssh", "browser"}

// SyntheticFrequencyTypes are the accepted schedule units.
var SyntheticFrequencyTypes = []string{"seconds", "minutes", "hours", "days", "weeks", "months", "cron"}

// SyntheticAuthTypes are the accepted authentication kinds.
var SyntheticAuthTypes = []string{"basic", "bearer", "secret"}

// SyntheticFrequencyAPI is the check schedule.
type SyntheticFrequencyAPI struct {
	FrequencyType string  `json:"type"`
	Interval      int64   `json:"interval"`
	Cron          string  `json:"cron"`
	Timezone      *string `json:"timezone,omitempty"`
}

// SyntheticAuthAPI is the tagged authentication payload. `type` selects which
// of the remaining fields apply.
type SyntheticAuthAPI struct {
	AuthType   string `json:"type"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Token      string `json:"token,omitempty"`
	SecretName string `json:"secret_name,omitempty"`
}

// SyntheticCookieAPI is one cookie injected before a browser journey runs.
type SyntheticCookieAPI struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	HTTPOnly bool   `json:"http_only"`
	Secure   bool   `json:"secure"`
}

// SyntheticVariableAPI is one variable injected into the probe environment.
type SyntheticVariableAPI struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Secure  bool   `json:"secure"`
	Example string `json:"example"`
}

// SyntheticAPI is the wire format of a synthetic check.
type SyntheticAPI struct {
	ID          string   `json:"id,omitempty"`
	OrgID       string   `json:"org_id,omitempty"`
	FolderID    string   `json:"folder_id,omitempty"`
	TZOffset    int32    `json:"tz_offset"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	CheckType   string   `json:"type"`
	Target      string   `json:"target"`

	// Config is type-specific, so it is carried through as written rather than
	// modelled five different ways.
	Config json.RawMessage `json:"config"`

	Frequency    SyntheticFrequencyAPI `json:"frequency"`
	Locations    []string              `json:"locations"`
	Enabled      bool                  `json:"enabled"`
	Destinations []string              `json:"destinations,omitempty"`

	Retries             int32 `json:"retries"`
	WaitBeforeRetrySecs int32 `json:"wait_before_retry_secs"`
	AlertIfFails        int32 `json:"alert_if_fails"`
	CooldownMins        int32 `json:"cooldown_mins"`

	CollectRUMData bool `json:"collect_rum_data"`
	SessionReplay  bool `json:"session_replay"`

	Auth      *SyntheticAuthAPI      `json:"auth,omitempty"`
	Cookies   []SyntheticCookieAPI   `json:"cookies,omitempty"`
	Variables []SyntheticVariableAPI `json:"variables,omitempty"`

	Start *int64 `json:"start,omitempty"`
}

// SyntheticListAPI wraps the synthetics list response.
type SyntheticListAPI struct {
	Checks []SyntheticAPI `json:"checks"`
}

// SyntheticLocationAPI is a probe location as the registry reports it.
//
// A check references a location by `id`, not by label. `status` matters as
// much as `enabled`: a private location with no agent checked in reads
// "pending", and a check assigned to it will not run.
type SyntheticLocationAPI struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Region      string   `json:"region"`
	Provider    string   `json:"provider"`
	Kind        string   `json:"kind"`
	Pool        string   `json:"pool"`
	Enabled     bool     `json:"enabled"`
	Types       []string `json:"types"`
	LiveAgents  int64    `json:"live_agents"`
	AgentNames  []string `json:"agent_names"`
	AgentsTotal int64    `json:"agents_total"`
	Status      string   `json:"status"`
}

// SyntheticDeviceAPI is one browser viewport a browser check can run against.
type SyntheticDeviceAPI struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

// SyntheticLocationListAPI wraps the locations list response.
//
// The endpoint answers with more than locations: it also reports the browsers
// and viewports available to a browser check, which is what a `config`
// document has to choose from.
type SyntheticLocationListAPI struct {
	Locations []SyntheticLocationAPI `json:"locations"`
	Browsers  []string               `json:"browsers"`
	Devices   []SyntheticDeviceAPI   `json:"devices"`
}

// CreateSynthetic creates a check and returns its ID.
//
// The folder comes from the query parameter, not from the body: the permission
// gate reads the query, so honouring a folder in the body would let a crafted
// request write into a folder the caller cannot reach.
func (c *Client) CreateSynthetic(ctx context.Context, orgID, folderID string, req SyntheticAPI) (string, error) {
	path := fmt.Sprintf("/api/%s/synthetics", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	// The create endpoint answers with the stored check itself rather than the
	// {code, message, id} envelope most of the API uses.
	var out SyntheticAPI
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// UpdateSynthetic replaces an existing check.
func (c *Client) UpdateSynthetic(ctx context.Context, orgID, id string, req SyntheticAPI) error {
	path := fmt.Sprintf("/api/%s/synthetics/%s", pathEscape(orgID), pathEscape(id))
	return c.do(ctx, http.MethodPut, path, req)
}

// GetSynthetic reads a check by ID. It returns (nil, nil) when absent.
func (c *Client) GetSynthetic(ctx context.Context, orgID, id string) (*SyntheticAPI, error) {
	path := fmt.Sprintf("/api/%s/synthetics/%s", pathEscape(orgID), pathEscape(id))
	var out SyntheticAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// ListSynthetics returns every check in an organization.
func (c *Client) ListSynthetics(ctx context.Context, orgID string) ([]SyntheticAPI, error) {
	var out SyntheticListAPI
	path := fmt.Sprintf("/api/%s/synthetics", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Checks, nil
}

// FindSyntheticByName resolves a check name to its entry.
func (c *Client) FindSyntheticByName(ctx context.Context, orgID, name string) (*SyntheticAPI, error) {
	checks, err := c.ListSynthetics(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i], nil
		}
	}
	return nil, nil
}

// SetSyntheticEnabled pauses or resumes a check. Enabling is its own endpoint
// rather than a field on the update body.
func (c *Client) SetSyntheticEnabled(ctx context.Context, orgID, id string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/synthetics/%s/enable", pathEscape(orgID), pathEscape(id))
	return c.do(ctx, http.MethodPut, path, map[string]bool{"enabled": enabled})
}

// MoveSynthetics relocates checks into another folder, which the update
// endpoint deliberately does not do.
func (c *Client) MoveSynthetics(ctx context.Context, orgID, dstFolderID string, ids []string) error {
	path := fmt.Sprintf("/api/v2/%s/synthetics/move", pathEscape(orgID))
	body := map[string]any{"synthetic_ids": ids, "dst_folder_id": dstFolderID}
	return c.do(ctx, http.MethodPatch, path, body)
}

// DeleteSynthetic removes a check.
func (c *Client) DeleteSynthetic(ctx context.Context, orgID, id string) error {
	path := fmt.Sprintf("/api/%s/synthetics/%s", pathEscape(orgID), pathEscape(id))
	return c.deleteIgnoreMissing(ctx, path)
}

// ListSyntheticLocations returns the probe locations available to an
// organization, along with the browsers and viewports a browser check can use.
func (c *Client) ListSyntheticLocations(ctx context.Context, orgID string) (*SyntheticLocationListAPI, error) {
	var out SyntheticLocationListAPI
	path := fmt.Sprintf("/api/%s/synthetics/locations", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// isSyntheticsDisabled reports whether an error is the 404 a server returns
// when synthetics is switched off.
//
// The routes are not registered when ZO_SYNTHETICS_ENABLED is false, so the
// failure is indistinguishable from a missing check unless the path itself is
// checked. Treating it as "feature off" only for collection endpoints, which
// always exist when the feature is on, keeps a genuinely absent check reading
// as absent.
func isSyntheticsDisabled(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	// A route that exists answers with a JSON envelope. An unregistered one
	// falls through to the catch-all, which does not.
	return !strings.Contains(apiErr.Body, "\"code\"")
}
