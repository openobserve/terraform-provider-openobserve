package provider

import (
	"context"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// Composite alerts
// ---------------------------------------------------------------------------
//
// A composite alert has no query of its own. It combines the durable states of
// other alerts through a boolean expression, so it never re-runs a child's
// query and costs nothing to evaluate.
//
// Composites share the alert endpoints rather than having their own: create and
// update are the ordinary alert routes discriminated by `alert_type`, and get,
// delete and move resolve the ID to a composite on the server. Only the request
// and response bodies differ, which is why they get their own types here.

// CompositeStalePolicies are the accepted `stale_child_policy` values.
var CompositeStalePolicies = []string{"use_last_state", "treat_as_false", "treat_as_true"}

// Limits the server enforces on a composite expression. Checking them during
// `plan` turns an apply-time 400 into a diagnostic against the offending line.
const (
	CompositeMinChildren      = 2
	CompositeMaxChildren      = 10
	CompositeMaxExpressionLen = 4 * 1024
)

// CompositeConditionAPI is the boolean expression and its truth policy.
type CompositeConditionAPI struct {
	Expression            string `json:"expression"`
	WarningCountsAsFiring bool   `json:"warning_counts_as_firing"`
	StaleChildPolicy      string `json:"stale_child_policy"`
}

// CompositeTriggerAPI is the only part of `trigger_condition` a composite
// accepts. Every other field, including `period` and `frequency`, is rejected
// outright rather than ignored: a composite runs when a child changes state,
// so a schedule of its own would be a lie. Sending the full trigger struct
// would trip that guard on the zero values alone.
type CompositeTriggerAPI struct {
	Silence int64 `json:"silence"`
}

// CompositeAlertWriteAPI is the create and update body for a composite alert.
type CompositeAlertWriteAPI struct {
	AlertType          string                `json:"alert_type"`
	CompositeCondition CompositeConditionAPI `json:"composite_condition"`
	Name               string                `json:"name"`
	Description        string                `json:"description"`
	Enabled            bool                  `json:"enabled"`
	Destinations       []string              `json:"destinations"`
	Template           *string               `json:"template,omitempty"`
	ContextAttrs       map[string]string     `json:"context_attributes,omitempty"`
	TriggerCondition   CompositeTriggerAPI   `json:"trigger_condition"`
	CreatesIncident    bool                  `json:"creates_incident,omitempty"`
	Workflows          []string              `json:"workflows,omitempty"`
	Priority           *int64                `json:"priority,omitempty"`
	Tags               []string              `json:"tags,omitempty"`
	Owner              *string               `json:"owner,omitempty"`
	PendingPeriodSec   int64                 `json:"pending_period_sec"`
}

// CompositeChildAPI is one resolved child in a composite's detail response.
type CompositeChildAPI struct {
	AlertID    string  `json:"alert_id"`
	Accessible bool    `json:"accessible"`
	Name       *string `json:"name,omitempty"`
	AlertType  *string `json:"alert_type,omitempty"`
	FolderID   *string `json:"folder_id,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
	Level      *string `json:"level,omitempty"`
	LevelAt    *int64  `json:"level_at,omitempty"`
	Stale      *bool   `json:"stale,omitempty"`
	Truth      *bool   `json:"truth,omitempty"`
}

// CompositeEvaluationAPI is the composite's own most recent verdict.
type CompositeEvaluationAPI struct {
	Result      bool   `json:"result"`
	Level       string `json:"level"`
	EvaluatedAt *int64 `json:"evaluated_at,omitempty"`
}

// CompositeAlertAPI is the wire format the composite detail endpoint returns.
//
// It differs from the create body in more than casing: the folder arrives as
// `folderId`, `silence` comes back in minutes derived from the stored seconds,
// and `owner` is not reported at all.
type CompositeAlertAPI struct {
	ID                  string                  `json:"id"`
	AlertType           string                  `json:"alert_type"`
	FolderID            string                  `json:"folderId"`
	Name                string                  `json:"name"`
	Description         *string                 `json:"description,omitempty"`
	Enabled             bool                    `json:"enabled"`
	Destinations        []string                `json:"destinations"`
	Template            *string                 `json:"template,omitempty"`
	ContextAttrs        map[string]string       `json:"context_attributes,omitempty"`
	TriggerCondition    CompositeTriggerAPI     `json:"trigger_condition"`
	CreatesIncident     bool                    `json:"creates_incident"`
	Workflows           []string                `json:"workflows,omitempty"`
	Priority            *int64                  `json:"priority,omitempty"`
	Tags                []string                `json:"tags,omitempty"`
	SchedulerJobPresent *bool                   `json:"scheduler_job_present,omitempty"`
	CompositeCondition  CompositeConditionAPI   `json:"composite_condition"`
	Children            []CompositeChildAPI     `json:"children"`
	Evaluation          *CompositeEvaluationAPI `json:"evaluation,omitempty"`
	PendingPeriodSec    int64                   `json:"pending_period_sec"`
}

// CompositeValidationWarningAPI is one advisory warning from the validate endpoint.
type CompositeValidationWarningAPI struct {
	Code    string `json:"code"`
	AlertID string `json:"alert_id"`
}

// CompositeValidationAPI is the response of the composite validate endpoint.
type CompositeValidationAPI struct {
	Valid               bool                            `json:"valid"`
	CanonicalExpression *string                         `json:"canonical_expression,omitempty"`
	Children            []CompositeChildAPI             `json:"children"`
	Result              *bool                           `json:"result,omitempty"`
	ResultLevel         *string                         `json:"result_level,omitempty"`
	Warnings            []CompositeValidationWarningAPI `json:"warnings"`
}

// CreateCompositeAlert creates a composite alert and returns its ID.
func (c *Client) CreateCompositeAlert(ctx context.Context, orgID, folderID string, req CompositeAlertWriteAPI) (string, error) {
	req.AlertType = "composite"
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

// UpdateCompositeAlert replaces an existing composite alert.
func (c *Client) UpdateCompositeAlert(ctx context.Context, orgID, alertID string, req CompositeAlertWriteAPI) error {
	req.AlertType = "composite"
	path := fmt.Sprintf("/api/v2/%s/alerts/%s", pathEscape(orgID), pathEscape(alertID))
	return c.do(ctx, http.MethodPut, path, req)
}

// GetCompositeAlert reads a composite alert by ID. It returns (nil, nil) when
// absent, and an error when the ID resolves to an ordinary alert instead.
func (c *Client) GetCompositeAlert(ctx context.Context, orgID, alertID string) (*CompositeAlertAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts/%s", pathEscape(orgID), pathEscape(alertID))
	var out CompositeAlertAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	if out.AlertType != "composite" {
		return nil, fmt.Errorf("alert %q is a %s alert, not a composite alert", alertID, out.AlertType)
	}
	return &out, nil
}

// ValidateCompositeExpression asks the server to parse an expression and
// resolve its children without persisting anything.
func (c *Client) ValidateCompositeExpression(ctx context.Context, orgID string, cond CompositeConditionAPI, compositeID string) (*CompositeValidationAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts/composites/validate", pathEscape(orgID))
	body := map[string]any{"composite_condition": cond}
	if compositeID != "" {
		body["composite_id"] = compositeID
	}
	var out CompositeValidationAPI
	if err := c.doJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CompositeReferenceAPI is one composite that references a given alert.
type CompositeReferenceAPI struct {
	AlertID  string `json:"alert_id"`
	Name     string `json:"name"`
	FolderID string `json:"folder_id"`
}

// CompositeReferencesAPI is the response of the composite-references endpoint.
type CompositeReferencesAPI struct {
	References []CompositeReferenceAPI `json:"references"`
	// HiddenReferenceCount counts referencing composites the caller may not
	// read. A non-zero value means `References` is incomplete, so a "nothing
	// references this" conclusion drawn from the list alone would be wrong.
	HiddenReferenceCount int64 `json:"hidden_reference_count"`
}

// ListCompositeReferences returns the composites that use the given alert as a
// child. The server refuses to delete a referenced alert, so a destroy that
// tears down a child and its parent in the wrong order needs this to explain
// itself.
func (c *Client) ListCompositeReferences(ctx context.Context, orgID, alertID string) (*CompositeReferencesAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/alerts/%s/composite-references", pathEscape(orgID), pathEscape(alertID))
	var out CompositeReferencesAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil || !found {
		return nil, err
	}
	return &out, nil
}
