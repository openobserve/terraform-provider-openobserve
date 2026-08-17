package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// Alert templates
// ---------------------------------------------------------------------------

// AlertTemplateAPI is the wire format of an alert template.
type AlertTemplateAPI struct {
	Name         string `json:"name"`
	Body         string `json:"body"`
	IsDefault    *bool  `json:"isDefault,omitempty"`
	IsPrebuilt   bool   `json:"isPrebuilt,omitempty"`
	TemplateType string `json:"type"`
	Title        string `json:"title"`
}

// CreateAlertTemplate creates a template.
func (c *Client) CreateAlertTemplate(ctx context.Context, orgID string, req AlertTemplateAPI) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/alerts/templates", pathEscape(orgID)), req)
}

// UpdateAlertTemplate replaces an existing template.
func (c *Client) UpdateAlertTemplate(ctx context.Context, orgID, name string, req AlertTemplateAPI) error {
	path := fmt.Sprintf("/api/%s/alerts/templates/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPut, path, req)
}

// GetAlertTemplate reads a template. It returns (nil, nil) when absent.
func (c *Client) GetAlertTemplate(ctx context.Context, orgID, name string) (*AlertTemplateAPI, error) {
	path := fmt.Sprintf("/api/%s/alerts/templates/%s", pathEscape(orgID), pathEscape(name))
	var out AlertTemplateAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// ListAlertTemplates returns every template in an organization.
func (c *Client) ListAlertTemplates(ctx context.Context, orgID string) ([]AlertTemplateAPI, error) {
	var out []AlertTemplateAPI
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/alerts/templates", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAlertTemplate removes a template.
func (c *Client) DeleteAlertTemplate(ctx context.Context, orgID, name string) error {
	path := fmt.Sprintf("/api/%s/alerts/templates/%s", pathEscape(orgID), pathEscape(name))
	return c.deleteIgnoreMissing(ctx, path)
}

// ---------------------------------------------------------------------------
// Alert destinations
// ---------------------------------------------------------------------------

// AlertDestinationAPI is the wire format of an alert destination.
type AlertDestinationAPI struct {
	Name                string            `json:"name"`
	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	SkipTLSVerify       bool              `json:"skip_tls_verify"`
	Headers             map[string]string `json:"headers,omitempty"`
	Template            *string           `json:"template,omitempty"`
	Emails              []string          `json:"emails"`
	SNSTopicARN         *string           `json:"sns_topic_arn,omitempty"`
	AWSRegion           *string           `json:"aws_region,omitempty"`
	DestinationType     string            `json:"type"`
	ActionID            *string           `json:"action_id,omitempty"`
	OutputFormat        json.RawMessage   `json:"output_format,omitempty"`
	DestinationTypeName *string           `json:"destination_type_name,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
}

// CreateAlertDestination creates a destination.
func (c *Client) CreateAlertDestination(ctx context.Context, orgID string, req AlertDestinationAPI) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/alerts/destinations", pathEscape(orgID)), req)
}

// UpdateAlertDestination replaces an existing destination.
func (c *Client) UpdateAlertDestination(ctx context.Context, orgID, name string, req AlertDestinationAPI) error {
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPut, path, req)
}

// GetAlertDestination reads a destination. It returns (nil, nil) when absent.
func (c *Client) GetAlertDestination(ctx context.Context, orgID, name string) (*AlertDestinationAPI, error) {
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	var out AlertDestinationAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// ListAlertDestinations returns every destination in an organization.
func (c *Client) ListAlertDestinations(ctx context.Context, orgID string) ([]AlertDestinationAPI, error) {
	var out []AlertDestinationAPI
	path := fmt.Sprintf("/api/%s/alerts/destinations", pathEscape(orgID))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAlertDestination removes a destination.
func (c *Client) DeleteAlertDestination(ctx context.Context, orgID, name string) error {
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	return c.deleteIgnoreMissing(ctx, path)
}

// ---------------------------------------------------------------------------
// Alerts (v2 API)
// ---------------------------------------------------------------------------

// AlertConditionAPI is a single comparison used by PromQL and aggregation alerts.
type AlertConditionAPI struct {
	Column   string          `json:"column"`
	Operator string          `json:"operator"`
	Value    json.RawMessage `json:"value"`
}

// AlertAggregationAPI configures aggregation for `custom` alerts.
type AlertAggregationAPI struct {
	GroupBy      []string          `json:"group_by,omitempty"`
	Function     string            `json:"function"`
	Having       AlertConditionAPI `json:"having"`
	WarningValue *float64          `json:"warning_value,omitempty"`
	MultiAlert   bool              `json:"multi_alert,omitempty"`
}

// AlertCompareHistoricData is one entry of multi_time_range.
type AlertCompareHistoricData struct {
	Offset string `json:"offSet"`
}

// AlertSloConditionAPI fires on an SLO's error budget or burn rate rather than
// on a query result.
type AlertSloConditionAPI struct {
	SloID           string   `json:"slo_id"`
	Kind            string   `json:"kind"`
	Operator        string   `json:"operator"`
	Critical        float64  `json:"critical"`
	Warning         *float64 `json:"warning,omitempty"`
	LongWindowSecs  *int64   `json:"long_window_secs,omitempty"`
	ShortWindowSecs *int64   `json:"short_window_secs,omitempty"`
	MultiAlert      bool     `json:"multi_alert,omitempty"`
}

// AlertQueryConditionAPI describes what an alert evaluates.
type AlertQueryConditionAPI struct {
	QueryType          string                     `json:"type"`
	Conditions         json.RawMessage            `json:"conditions,omitempty"`
	SQL                *string                    `json:"sql,omitempty"`
	PromQL             *string                    `json:"promql,omitempty"`
	PromQLCondition    *AlertConditionAPI         `json:"promql_condition,omitempty"`
	PromQLWarningValue *float64                   `json:"promql_warning_value,omitempty"`
	PromQLMultiAlert   bool                       `json:"promql_multi_alert,omitempty"`
	Aggregation        *AlertAggregationAPI       `json:"aggregation,omitempty"`
	VRLFunction        *string                    `json:"vrl_function,omitempty"`
	SearchEventType    *string                    `json:"search_event_type,omitempty"`
	MultiTimeRange     []AlertCompareHistoricData `json:"multi_time_range,omitempty"`
	SloCondition       *AlertSloConditionAPI      `json:"slo_condition,omitempty"`
}

// AlertDeduplicationAPI collapses repeated firings of the same underlying issue.
type AlertDeduplicationAPI struct {
	Enabled           bool     `json:"enabled"`
	FingerprintFields []string `json:"fingerprint_fields,omitempty"`
	TimeWindowMinutes *int64   `json:"time_window_minutes,omitempty"`
}

// AlertTriggerConditionAPI describes when and how often an alert fires.
type AlertTriggerConditionAPI struct {
	Period           int64   `json:"period"`
	Operator         string  `json:"operator"`
	Threshold        int64   `json:"threshold"`
	WarningThreshold *int64  `json:"warning_threshold,omitempty"`
	NotifyOnWarning  *bool   `json:"notify_on_warning,omitempty"`
	Frequency        int64   `json:"frequency"`
	Cron             string  `json:"cron"`
	FrequencyType    string  `json:"frequency_type"`
	Silence          int64   `json:"silence"`
	Timezone         *string `json:"timezone,omitempty"`
	ToleranceInSecs  *int64  `json:"tolerance_in_secs,omitempty"`
	AlignTime        bool    `json:"align_time"`
}

// AlertAPI is the wire format of an alert.
type AlertAPI struct {
	ID               string                   `json:"id,omitempty"`
	Name             string                   `json:"name"`
	OrgID            string                   `json:"org_id,omitempty"`
	StreamType       string                   `json:"stream_type"`
	StreamName       string                   `json:"stream_name"`
	IsRealTime       bool                     `json:"is_real_time"`
	QueryCondition   AlertQueryConditionAPI   `json:"query_condition"`
	TriggerCondition AlertTriggerConditionAPI `json:"trigger_condition"`
	Destinations     []string                 `json:"destinations"`
	Template         *string                  `json:"template,omitempty"`
	ContextAttrs     map[string]string        `json:"context_attributes,omitempty"`
	RowTemplate      string                   `json:"row_template"`
	RowTemplateType  string                   `json:"row_template_type,omitempty"`
	Description      string                   `json:"description"`
	Enabled          bool                     `json:"enabled"`
	TZOffset         int32                    `json:"tz_offset"`
	Owner            *string                  `json:"owner,omitempty"`
	LastTriggeredAt  *int64                   `json:"last_triggered_at,omitempty"`
	LastSatisfiedAt  *int64                   `json:"last_satisfied_at,omitempty"`
	UpdatedAt        *int64                   `json:"updated_at,omitempty"`
	LastEditedBy     *string                  `json:"last_edited_by,omitempty"`
	Priority         *int64                   `json:"priority,omitempty"`
	Tags             []string                 `json:"tags,omitempty"`
	FolderID         *string                  `json:"folder_id,omitempty"`
	CreatesIncident  bool                     `json:"creates_incident,omitempty"`
	Workflows        []string                 `json:"workflows,omitempty"`
	Deduplication    *AlertDeduplicationAPI   `json:"deduplication,omitempty"`
}

// AlertListItemAPI is one entry of the alerts list response.
type AlertListItemAPI struct {
	AlertID     string   `json:"alert_id"`
	FolderID    string   `json:"folder_id"`
	FolderName  string   `json:"folder_name"`
	Name        string   `json:"name"`
	Owner       *string  `json:"owner,omitempty"`
	Description *string  `json:"description,omitempty"`
	AlertType   string   `json:"alert_type"`
	Enabled     bool     `json:"enabled"`
	IsRealTime  bool     `json:"is_real_time"`
	Tags        []string `json:"tags,omitempty"`
}

// AlertListAPI wraps the alerts list response.
type AlertListAPI struct {
	List []AlertListItemAPI `json:"list"`
}

// CreateAlert creates an alert in the given folder and returns its ID.
func (c *Client) CreateAlert(ctx context.Context, orgID, folderID string, req AlertAPI) (string, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	var out MessageResponse
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// GetAlert reads an alert by ID. It returns (nil, nil) when absent.
func (c *Client) GetAlert(ctx context.Context, orgID, alertID string) (*AlertAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts/%s", pathEscape(orgID), pathEscape(alertID))
	var out AlertAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// UpdateAlert replaces an existing alert.
func (c *Client) UpdateAlert(ctx context.Context, orgID, alertID string, req AlertAPI) error {
	path := fmt.Sprintf("/api/v2/%s/alerts/%s", pathEscape(orgID), pathEscape(alertID))
	return c.do(ctx, http.MethodPut, path, req)
}

// MoveAlerts relocates alerts into another folder.
//
// The update endpoint deliberately leaves an alert's folder alone, so changing
// where an alert lives is a separate call.
func (c *Client) MoveAlerts(ctx context.Context, orgID, dstFolderID string, alertIDs []string) error {
	path := fmt.Sprintf("/api/v2/%s/alerts/move", pathEscape(orgID))
	body := map[string]any{
		"alert_ids":          alertIDs,
		"anomaly_config_ids": []string{},
		"dst_folder_id":      dstFolderID,
	}
	return c.do(ctx, http.MethodPatch, path, body)
}

// DeleteAlert removes an alert.
func (c *Client) DeleteAlert(ctx context.Context, orgID, alertID string) error {
	path := fmt.Sprintf("/api/v2/%s/alerts/%s", pathEscape(orgID), pathEscape(alertID))
	return c.deleteIgnoreMissing(ctx, path)
}

// ListAlerts returns the alerts of an organization, optionally scoped to one folder.
func (c *Client) ListAlerts(ctx context.Context, orgID, folderID string) ([]AlertListItemAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	var out AlertListAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// FindAlertByName resolves an alert name to its list entry, searching all folders.
func (c *Client) FindAlertByName(ctx context.Context, orgID, folderID, name string) (*AlertListItemAPI, error) {
	alerts, err := c.ListAlerts(ctx, orgID, folderID)
	if err != nil {
		return nil, err
	}
	for i := range alerts {
		if alerts[i].Name == name {
			return &alerts[i], nil
		}
	}
	return nil, nil
}
