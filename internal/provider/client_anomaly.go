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
// Anomaly alerts
// ---------------------------------------------------------------------------
//
// An anomaly alert is an anomaly detection config, served by its own API at
// /api/{org_id}/anomaly_detection rather than by the v2 alerts API. The v2
// alerts endpoint accepts `alert_type = "anomaly_detection"` only as a
// delegate that writes the same config, so the provider talks to the config
// API directly. The routes exist only in OpenObserve Enterprise.
//
// A config is trained on the stream's history and then flags each bucket whose
// value leaves the band `expected ± k·σ`. The fields the server accepts but
// ignores (`seasonality`, the `rcf_*` sizes, `level_half_width_seconds`) and the
// legacy `percentile`/`threshold` are never sent.

// AnomalyFilterAPI is one row of a filters-mode query.
type AnomalyFilterAPI struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// AnomalyConfigWriteAPI is the body of a create or an update.
//
// The nullable settings carry no `omitempty`. On an update the server reads an
// explicit `null` as "clear back to the default" and an absent field as "keep
// the stored value". `band_width`, `alert_budget_per_day`,
// `alert_window_recover_pct`, `priority` and `detection_function_field` have no
// schema default, so removing one from the configuration sends `null`, which
// clears it. The other band settings always carry their default. On a create
// `null` and absent mean the same thing.
//
// `schedule_interval` is omitted on an update that does not change it: the
// server resets the detection trigger to run now whenever the field is sent.
//
// `filters` is a pointer so that filters mode can send the empty list it
// requires while custom_sql mode sends nothing.
//
// `tags` and `alert_destinations` are lists the create body cannot take as
// `null`, so they are always sent, empty when unset; on an update an empty list
// clears them.
type AnomalyConfigWriteAPI struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Only on create: the stream of a config cannot change.
	StreamName string `json:"stream_name,omitempty"`
	StreamType string `json:"stream_type,omitempty"`

	QueryMode              string              `json:"query_mode"`
	Filters                *[]AnomalyFilterAPI `json:"filters,omitempty"`
	CustomSQL              *string             `json:"custom_sql,omitempty"`
	DetectionFunction      string              `json:"detection_function"`
	DetectionFunctionField *string             `json:"detection_function_field"`
	HistogramInterval      string              `json:"histogram_interval"`
	ScheduleInterval       string              `json:"schedule_interval,omitempty"`
	DetectionWindowSeconds int64               `json:"detection_window_seconds"`
	TrainingWindowDays     int64               `json:"training_window_days"`
	RetrainIntervalDays    int64               `json:"retrain_interval_days"`

	AlertBudgetPerDay     *float64 `json:"alert_budget_per_day"`
	BandWidth             *float64 `json:"band_width"`
	AlertDirection        *string  `json:"alert_direction"`
	AlertWindowBuckets    *int64   `json:"alert_window_buckets"`
	AlertWindowFirePct    *float64 `json:"alert_window_fire_pct"`
	AlertWindowRecoverPct *float64 `json:"alert_window_recover_pct"`

	AlertEnabled      bool     `json:"alert_enabled"`
	AlertDestinations []string `json:"alert_destinations"`
	Enabled           bool     `json:"enabled"`
	FolderID          string   `json:"folder_id"`
	Owner             *string  `json:"owner,omitempty"`
	Priority          *int64   `json:"priority"`
	Tags              []string `json:"tags"`
}

// AnomalyConfigAPI is a config as GET returns it.
type AnomalyConfigAPI struct {
	AnomalyID              string          `json:"anomaly_id"`
	OrgID                  string          `json:"org_id"`
	Name                   string          `json:"name"`
	Description            *string         `json:"description"`
	StreamName             string          `json:"stream_name"`
	StreamType             string          `json:"stream_type"`
	QueryMode              string          `json:"query_mode"`
	Filters                json.RawMessage `json:"filters"`
	CustomSQL              *string         `json:"custom_sql"`
	DetectionFunction      string          `json:"detection_function"`
	HistogramInterval      string          `json:"histogram_interval"`
	ScheduleInterval       string          `json:"schedule_interval"`
	DetectionWindowSeconds int64           `json:"detection_window_seconds"`
	TrainingWindowDays     int64           `json:"training_window_days"`
	RetrainIntervalDays    int64           `json:"retrain_interval_days"`

	AlertBudgetPerDay     *float64 `json:"alert_budget_per_day"`
	BandWidth             *float64 `json:"band_width"`
	AlertDirection        *string  `json:"alert_direction"`
	AlertWindowBuckets    *int64   `json:"alert_window_buckets"`
	AlertWindowFirePct    *float64 `json:"alert_window_fire_pct"`
	AlertWindowRecoverPct *float64 `json:"alert_window_recover_pct"`

	AlertEnabled      bool     `json:"alert_enabled"`
	AlertDestinations []string `json:"alert_destinations"`
	Enabled           bool     `json:"enabled"`
	FolderID          string   `json:"folder_id"`
	Owner             *string  `json:"owner"`
	Priority          *int64   `json:"priority"`
	Tags              []string `json:"tags"`

	// Written by training.
	BandGrouping        *string  `json:"band_grouping"`
	BandK               *float64 `json:"band_k"`
	TrainingDataStartUs *int64   `json:"training_data_start_us"`
	TrainingDataEndUs   *int64   `json:"training_data_end_us"`
	IsTrained           bool     `json:"is_trained"`
	Status              string   `json:"status"`
	LastError           *string  `json:"last_error"`
	// HasBandSettings is false on a server older than the band settings,
	// which leaves their columns out of the response altogether.
	HasBandSettings bool `json:"-"`
}

// anomalyCreatedAPI is the part of a create response the provider needs. The
// rest is re-read with GET, which is the only response that carries the
// training span and the status as a label.
type anomalyCreatedAPI struct {
	AnomalyID string `json:"anomaly_id"`
}

// errAnomalyUnsupported marks a server without the anomaly detection API.
var errAnomalyUnsupported = errors.New("anomaly detection API not available")

// anomalyRouteMissing reports whether err is the 404 a server without the
// anomaly detection API answers.
//
// An open-source server has no anomaly routes, and its catch-all answers any
// unknown path with 404 "Organization not found", the same answer an
// Enterprise server gives for an organization that does not exist. The body
// cannot tell the two apart, so the edition in the public /config document
// does. A 404 naming a config ("Anomaly config not found") is a missing object
// on a server that has the API. When /config cannot be read the answer is
// false, so the server's own error is reported rather than a guess.
func (c *Client) anomalyRouteMissing(ctx context.Context, err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	if strings.Contains(strings.ToLower(apiErr.Body), "anomaly config not found") {
		return false
	}
	var cfg struct {
		BuildType string `json:"build_type"`
	}
	if cfgErr := c.doJSON(ctx, http.MethodGet, "/config", nil, &cfg); cfgErr != nil || cfg.BuildType == "" {
		return false
	}
	return cfg.BuildType != "enterprise"
}

// unsupportedOr wraps err as errAnomalyUnsupported when it comes from a
// server without the anomaly detection API.
func (c *Client) unsupportedOr(ctx context.Context, err error) error {
	if c.anomalyRouteMissing(ctx, err) {
		return fmt.Errorf("%w: %v", errAnomalyUnsupported, err)
	}
	return err
}

// anomalyErrorDiag turns an anomaly API error into a diagnostic that says what
// is actually wrong when the server has no anomaly detection at all.
func anomalyErrorDiag(action string, err error) (string, string) {
	if errors.Is(err, errAnomalyUnsupported) {
		return "Anomaly alerts require OpenObserve Enterprise",
			"The server has no anomaly detection API (/api/{org_id}/anomaly_detection answered HTTP 404). " +
				"Anomaly alerts are an OpenObserve Enterprise feature, and the band settings this resource " +
				"manages need OpenObserve v" + anomalyMinVersion + " or later. This resource cannot be used " +
				"against an open-source deployment.\n\n" + err.Error()
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden &&
		strings.Contains(apiErr.Body, "Anomaly detection is disabled") {
		return "Anomaly detection is disabled on this server",
			"OpenObserve answered 403 \"Anomaly detection is disabled\". The server was started with " +
				"O2_ANOMALY_DETECTION_DISABLED=true, which turns every anomaly detection endpoint off."
	}
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		return "Error " + action + " anomaly alert: unauthorized",
			"OpenObserve answered 401 Unauthorized. Either the provider's credentials are wrong, or the " +
				"organization in `org_id` does not exist or the user is not a member of it: OpenObserve answers " +
				"an organization the user cannot see the same way as bad credentials.\n\n" + err.Error()
	}
	return "Error " + action + " anomaly alert", err.Error()
}

// CreateAnomalyConfig creates an anomaly alert and returns its ID.
func (c *Client) CreateAnomalyConfig(ctx context.Context, orgID string, req AnomalyConfigWriteAPI) (string, error) {
	path := fmt.Sprintf("/api/%s/anomaly_detection", pathEscape(orgID))
	var out anomalyCreatedAPI
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", c.unsupportedOr(ctx, err)
	}
	if out.AnomalyID == "" {
		return "", fmt.Errorf("OpenObserve created the anomaly alert but returned no anomaly_id")
	}
	return out.AnomalyID, nil
}

// GetAnomalyConfig reads an anomaly alert. It returns (nil, nil) when absent.
func (c *Client) GetAnomalyConfig(ctx context.Context, orgID, anomalyID string) (*AnomalyConfigAPI, error) {
	path := fmt.Sprintf("/api/%s/anomaly_detection/%s", pathEscape(orgID), pathEscape(anomalyID))
	var raw json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &raw); err != nil {
		if err := c.unsupportedOr(ctx, err); errors.Is(err, errAnomalyUnsupported) {
			return nil, err
		}
		// Any other 404, the config's or its organization's, means it is gone.
		if isNotFound(err) {
			return nil, nil
		}
		if c.anomalyGoneBehind403(ctx, orgID, anomalyID, err) {
			return nil, nil
		}
		return nil, err
	}
	return decodeAnomalyConfig(raw)
}

// anomalyGoneBehind403 reports whether a 403 on one config means the config no
// longer exists. With RBAC enforced, OpenObserve answers "Unauthorized Access"
// rather than 404 for an ID that has no permission tuple, which is every ID
// once it is deleted. The organization's list is the tie-breaker: a config
// missing from it is gone, and one present in it is a real permission error.
func (c *Client) anomalyGoneBehind403(ctx context.Context, orgID, anomalyID string, err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden ||
		!strings.Contains(apiErr.Body, "Unauthorized Access") {
		return false
	}
	configs, listErr := c.ListAnomalyConfigs(ctx, orgID)
	if listErr != nil {
		return false
	}
	for _, cfg := range configs {
		if cfg.AnomalyID == anomalyID {
			return false
		}
	}
	return true
}

// decodeAnomalyConfig decodes one config and notes whether the server knows
// the band settings.
func decodeAnomalyConfig(raw json.RawMessage) (*AnomalyConfigAPI, error) {
	// Only a 404 or a 403 for a deleted config means "gone". A 200 that holds
	// no config is an error, so a bad response never drops the resource.
	var out AnomalyConfigAPI
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parsing anomaly alert: %w (body: %s)", err, truncate(string(raw), 512))
	}
	if out.AnomalyID == "" {
		return nil, fmt.Errorf("OpenObserve returned an anomaly alert without anomaly_id (body: %s)", truncate(string(raw), 512))
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err == nil {
		_, out.HasBandSettings = keys["alert_window_fire_pct"]
	}
	return &out, nil
}

// UpdateAnomalyConfig writes every managed field of an anomaly alert.
func (c *Client) UpdateAnomalyConfig(ctx context.Context, orgID, anomalyID string, req AnomalyConfigWriteAPI) error {
	path := fmt.Sprintf("/api/%s/anomaly_detection/%s", pathEscape(orgID), pathEscape(anomalyID))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteAnomalyConfig removes an anomaly alert; a missing one is not an error.
func (c *Client) DeleteAnomalyConfig(ctx context.Context, orgID, anomalyID string) error {
	path := fmt.Sprintf("/api/%s/anomaly_detection/%s", pathEscape(orgID), pathEscape(anomalyID))
	if err := c.deleteIgnoreMissing(ctx, path); err != nil {
		if c.anomalyGoneBehind403(ctx, orgID, anomalyID, err) {
			return nil
		}
		return err
	}
	return nil
}

// ListAnomalyConfigs returns every anomaly alert of an organization.
func (c *Client) ListAnomalyConfigs(ctx context.Context, orgID string) ([]AnomalyConfigAPI, error) {
	path := fmt.Sprintf("/api/%s/anomaly_detection", pathEscape(orgID))
	var out []AnomalyConfigAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, c.unsupportedOr(ctx, err)
	}
	return out, nil
}

// AnomalyBandSettingsSupported reports whether the server stores the band
// settings, judged from the configs it already lists: a server older than
// them leaves their columns out. known is false when the organization has no
// config to judge by, or when the caller may create alerts but not list them
// (403); the read-back after create then decides.
func (c *Client) AnomalyBandSettingsSupported(ctx context.Context, orgID string) (known, supported bool, err error) {
	path := fmt.Sprintf("/api/%s/anomaly_detection", pathEscape(orgID))
	var rows []map[string]json.RawMessage
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &rows); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden &&
			!strings.Contains(apiErr.Body, "Anomaly detection is disabled") {
			return false, false, nil
		}
		return false, false, c.unsupportedOr(ctx, err)
	}
	if len(rows) == 0 {
		return false, false, nil
	}
	_, supported = rows[0]["alert_window_fire_pct"]
	return true, supported, nil
}
