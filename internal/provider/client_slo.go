package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// SLOs
// ---------------------------------------------------------------------------
//
// The wire format is heavily flattened. An SLO's service level indicator is an
// adjacently tagged enum — `{"sli_type": "count", "config": {…}}` — and for a
// count SLI the config is itself adjacently tagged by `mode`. Both tags sit at
// the level the server expects rather than nested under a wrapper, so the Go
// types mirror that shape directly.

// SloCountSingleQueryAPI counts good and total rows in one scan, which is the
// only form where the two are provably drawn from the same rows.
type SloCountSingleQueryAPI struct {
	Stream     string  `json:"stream"`
	StreamType string  `json:"stream_type"`
	Scope      *string `json:"scope,omitempty"`
	GoodExpr   string  `json:"good_expr"`
}

// SloCountQueryAPI is one half of a dual-query count source.
type SloCountQueryAPI struct {
	Stream     string `json:"stream"`
	StreamType string `json:"stream_type"`
	SQL        string `json:"sql"`
}

// SloCountDualQueryAPI counts good and total with separate queries.
type SloCountDualQueryAPI struct {
	Good  SloCountQueryAPI `json:"good"`
	Total SloCountQueryAPI `json:"total"`
}

// SloCountPromQLAPI counts with a pair of PromQL expressions.
type SloCountPromQLAPI struct {
	Good  string `json:"good"`
	Total string `json:"total"`
}

// SloTimeSliceConfigAPI marks each slice good or bad by comparing an aggregate
// against a threshold.
type SloTimeSliceConfigAPI struct {
	Stream        string  `json:"stream"`
	StreamType    string  `json:"stream_type"`
	QueryLanguage string  `json:"query_language"`
	Query         string  `json:"query"`
	Scope         *string `json:"scope,omitempty"`
	Comparator    string  `json:"comparator"`
	Threshold     float64 `json:"threshold"`
	AbsentIsBad   bool    `json:"absent_is_bad,omitempty"`
}

// SloAlertConfigAPI derives the indicator from an existing alert's firing state.
type SloAlertConfigAPI struct {
	AlertID string `json:"alert_id"`
}

// SloAPI is the wire format of an SLO. `sli_type` and `config` are the enum tag
// and content; `config` is left raw so each indicator type can be marshalled
// into it without a union type.
type SloAPI struct {
	ID          string          `json:"id,omitempty"`
	Org         string          `json:"org,omitempty"`
	FolderID    string          `json:"folder_id,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	SliType     string          `json:"sli_type"`
	Config      json.RawMessage `json:"config"`
	GroupBy     []string        `json:"group_by,omitempty"`
	WindowSecs  int64           `json:"window_secs"`
	SliceSecs   int64           `json:"slice_interval_secs"`
	Target      float64         `json:"target"`
	Tags        []string        `json:"tags,omitempty"`
	Enabled     bool            `json:"enabled"`
	Owner       *string         `json:"owner,omitempty"`

	// Server-assigned; never sent on a write.
	DefinitionGeneration int    `json:"definition_generation,omitempty"`
	GroupsEstimate       *int64 `json:"groups_estimate,omitempty"`
	GroupsReserved       int64  `json:"groups_reserved,omitempty"`
}

// SloStatusAPI is the current measurement attached to a listed SLO. It is
// absent until the first evaluation pass has run: "not yet measured" and
// "measured as zero" are different answers.
//
// The derived figures are all optional because an SLO whose coverage has
// fallen below the floor is frozen — neither healthy nor breached — and the
// server reports that by omitting them rather than by sending zeros.
type SloStatusAPI struct {
	GroupKey             string   `json:"group_key"`
	Coverage             float64  `json:"coverage"`
	NoData               bool     `json:"no_data"`
	SLI                  *float64 `json:"sli,omitempty"`
	ErrorBudgetRemaining *float64 `json:"error_budget_remaining,omitempty"`
	BurnRate             *float64 `json:"burn_rate,omitempty"`
	TimeToExhaustSecs    *int64   `json:"time_to_exhaust_secs,omitempty"`
	Good                 float64  `json:"good"`
	Total                float64  `json:"total"`
	CoveredSlices        int64    `json:"covered_slices"`
	ComputedAt           *int64   `json:"computed_at,omitempty"`
}

// SloListItemAPI is one entry of the SLO list response: the SLO plus its status.
type SloListItemAPI struct {
	SloAPI
	Status *SloStatusAPI `json:"status,omitempty"`
}

// SloListAPI wraps the SLO list response.
type SloListAPI struct {
	List []SloListItemAPI `json:"list"`
}

// CreateSlo creates an SLO and returns its server-assigned ID.
func (c *Client) CreateSlo(ctx context.Context, orgID string, req SloAPI) (string, error) {
	var out MessageResponse
	path := fmt.Sprintf("/api/%s/slos", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// GetSlo reads an SLO by ID. It returns (nil, nil) when absent.
func (c *Client) GetSlo(ctx context.Context, orgID, sloID string) (*SloAPI, error) {
	path := fmt.Sprintf("/api/%s/slos/%s", pathEscape(orgID), pathEscape(sloID))
	var out SloAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// UpdateSlo replaces an SLO definition.
func (c *Client) UpdateSlo(ctx context.Context, orgID, sloID string, req SloAPI) error {
	path := fmt.Sprintf("/api/%s/slos/%s", pathEscape(orgID), pathEscape(sloID))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteSlo removes an SLO.
func (c *Client) DeleteSlo(ctx context.Context, orgID, sloID string) error {
	path := fmt.Sprintf("/api/%s/slos/%s", pathEscape(orgID), pathEscape(sloID))
	return c.deleteIgnoreMissing(ctx, path)
}

// ListSlos returns the SLOs of an organization, optionally scoped to one
// alert folder. SLOs live in alert folders; there is no separate SLO folder type.
func (c *Client) ListSlos(ctx context.Context, orgID, folderID string) ([]SloListItemAPI, error) {
	path := fmt.Sprintf("/api/%s/slos", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	var out SloListAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// FindSloByName resolves an SLO name to its list entry.
func (c *Client) FindSloByName(ctx context.Context, orgID, folderID, name string) (*SloListItemAPI, error) {
	slos, err := c.ListSlos(ctx, orgID, folderID)
	if err != nil {
		return nil, err
	}
	for i := range slos {
		if slos[i].Name == name {
			return &slos[i], nil
		}
	}
	return nil, nil
}

// SetSloEnabled enables or pauses an SLO. Enablement is its own endpoint rather
// than a field on the update body.
func (c *Client) SetSloEnabled(ctx context.Context, orgID, sloID string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/slos/%s/enable?value=%t", pathEscape(orgID), pathEscape(sloID), enabled)
	return c.do(ctx, http.MethodPut, path, nil)
}

// MoveSlos relocates SLOs into another alert folder.
func (c *Client) MoveSlos(ctx context.Context, orgID, dstFolderID string, sloIDs []string) error {
	path := fmt.Sprintf("/api/%s/slos/move", pathEscape(orgID))
	body := map[string]any{
		"slo_ids":       sloIDs,
		"dst_folder_id": dstFolderID,
	}
	return c.do(ctx, http.MethodPost, path, body)
}

// GetSloGroups returns the per-group breakdown of a grouped SLO.
func (c *Client) GetSloGroups(ctx context.Context, orgID, sloID string) (json.RawMessage, error) {
	path := fmt.Sprintf("/api/%s/slos/%s/groups", pathEscape(orgID), pathEscape(sloID))
	var out json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
