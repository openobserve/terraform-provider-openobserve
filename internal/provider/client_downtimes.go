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
// Downtimes
// ---------------------------------------------------------------------------
//
// A downtime is a maintenance window that mutes alerts, anomaly detections and
// synthetics checks, and corrects SLOs, while it is active. It is served by
// /api/v2/{org_id}/downtimes, an OpenObserve Enterprise feature that first
// shipped in v1.2.0-rc1. An open-source server has the routes but answers
// every one with 403 "Not Supported".
//
// The server keeps a downtime after it ends or is cancelled, for history, and
// refuses to delete one that is active or scheduled: it has to be cancelled
// first.

// DowntimeTargetFoldersAPI says which folders of a module a target covers.
type DowntimeTargetFoldersAPI struct {
	Kind      string   `json:"kind"` // "all" or "some"
	FolderIDs []string `json:"folder_ids,omitempty"`
}

// DowntimeTargetAPI is one module a downtime covers. Each present field
// narrows the module's items.
type DowntimeTargetAPI struct {
	Module       string                   `json:"module"`
	Folders      DowntimeTargetFoldersAPI `json:"folders"`
	Tags         []string                 `json:"tags,omitempty"`
	IDs          []string                 `json:"ids,omitempty"`
	SloMode      *string                  `json:"slo_mode,omitempty"`
	IncidentMode string                   `json:"incident_mode,omitempty"`
}

// DowntimeScheduleAPI is when a downtime applies. Times are microseconds UTC.
type DowntimeScheduleAPI struct {
	Repeat         string  `json:"repeat"`
	StartsAt       int64   `json:"starts_at"`
	EndsAt         *int64  `json:"ends_at,omitempty"`
	Timezone       string  `json:"timezone"`
	StartTimeLocal *string `json:"start_time_local,omitempty"`
	DurationSecs   int64   `json:"duration_secs"`
	Weekdays       []int64 `json:"weekdays"`
}

// DowntimeNotificationEventsAPI says which downtime events are announced.
type DowntimeNotificationEventsAPI struct {
	Started    bool `json:"started"`
	EndingSoon bool `json:"ending_soon"`
	Ended      bool `json:"ended"`
	Cancelled  bool `json:"cancelled"`
	Extended   bool `json:"extended"`
}

// DowntimeNotificationsAPI is who hears about a downtime's events.
type DowntimeNotificationsAPI struct {
	Destinations       []string                      `json:"destinations"`
	Events             DowntimeNotificationEventsAPI `json:"events"`
	EndingSoonLeadSecs int64                         `json:"ending_soon_lead_secs"`
	// Continues is set by the server on the follow-up a recurring
	// downtime's extension creates; a request never sets it.
	Continues *string `json:"continues,omitempty"`
}

// DowntimeAPI is a downtime as the server stores it, and the body of a create
// or an update, which take the same fields.
type DowntimeAPI struct {
	ID            string                    `json:"id,omitempty"`
	FolderID      string                    `json:"folder_id"`
	Name          string                    `json:"name"`
	Reason        *string                   `json:"reason,omitempty"`
	Condition     json.RawMessage           `json:"condition,omitempty"`
	Targets       []DowntimeTargetAPI       `json:"targets"`
	Schedule      DowntimeScheduleAPI       `json:"schedule"`
	ShowBanner    bool                      `json:"show_banner"`
	Notifications *DowntimeNotificationsAPI `json:"notifications,omitempty"`
	// Version is the optimistic lock: an update naming an older version than
	// the stored one is refused with 409.
	Version int64 `json:"version,omitempty"`
	// AlertGroups and EndOnClear belong to the quick mutes an alert's page
	// creates. They have no attribute; an update sends back what the server
	// holds, since the update replaces them.
	AlertGroups json.RawMessage `json:"alert_groups,omitempty"`
	EndOnClear  json.RawMessage `json:"end_on_clear,omitempty"`

	// Read only.
	CancelledAt *int64  `json:"cancelled_at,omitempty"`
	Status      string  `json:"status,omitempty"`
	CreatedBy   string  `json:"created_by,omitempty"`
	UpdatedBy   string  `json:"updated_by,omitempty"`
	CreatedAt   int64   `json:"created_at,omitempty"`
	UpdatedAt   int64   `json:"updated_at,omitempty"`
	Source      *string `json:"source,omitempty"`
}

// downtimeWriteAPI is the request body of a create or an update: the fields of
// DowntimeAPI the server reads.
type downtimeWriteAPI struct {
	FolderID      string                    `json:"folder_id"`
	Name          string                    `json:"name"`
	Reason        *string                   `json:"reason,omitempty"`
	Condition     json.RawMessage           `json:"condition,omitempty"`
	Targets       []DowntimeTargetAPI       `json:"targets"`
	Schedule      DowntimeScheduleAPI       `json:"schedule"`
	ShowBanner    bool                      `json:"show_banner"`
	Notifications *DowntimeNotificationsAPI `json:"notifications,omitempty"`
	Version       *int64                    `json:"version,omitempty"`
	AlertGroups   json.RawMessage           `json:"alert_groups,omitempty"`
	EndOnClear    json.RawMessage           `json:"end_on_clear,omitempty"`
}

// errDowntimesUnsupported marks a server without downtimes.
var errDowntimesUnsupported = errors.New("downtimes are not available on this server")

// downtimeUnsupportedOr wraps err as errDowntimesUnsupported when it is the
// answer of a server that has no downtimes: 403 "Not Supported" from an
// open-source build, or a 404 for the route itself from a release older than
// v1.2.0-rc1. A 404 naming a downtime ("Downtime not found") is a missing
// object on a server that has the API.
func downtimeUnsupportedOr(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	body := strings.ToLower(apiErr.Body)
	switch {
	case apiErr.StatusCode == http.StatusForbidden && strings.Contains(body, "not supported"),
		apiErr.StatusCode == http.StatusNotFound && !strings.Contains(body, "downtime not found"):
		return fmt.Errorf("%w: %v", errDowntimesUnsupported, err)
	}
	return err
}

// downtimeErrorDiag turns a downtime API error into a diagnostic that says
// what is actually wrong when the server has no downtimes at all.
func downtimeErrorDiag(action string, err error) (string, string) {
	if errors.Is(err, errDowntimesUnsupported) {
		return "Downtimes require OpenObserve Enterprise v1.2.0-rc1 or later",
			"The server has no downtimes API. Downtimes are an OpenObserve Enterprise feature that first " +
				"shipped in v1.2.0-rc1, so this resource cannot be used against an open-source deployment or " +
				"an older release.\n\n" + err.Error()
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden &&
		strings.Contains(apiErr.Body, "Downtimes are not enabled") {
		return "Downtimes are disabled on this server",
			"OpenObserve answered 403 \"Downtimes are not enabled\". The server runs with downtimes switched " +
				"off (O2_DOWNTIMES_ENABLED=false).\n\n" + err.Error()
	}
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
		return "Error " + action + " downtime: permission denied",
			"OpenObserve answered 403. The provider's user needs permission on downtimes: a role that " +
				"grants the `downtimes` resource, and write access to the downtime folder (`downtime_folders`) " +
				"for a create or a move.\n\n" + err.Error()
	}
	return "Error " + action + " downtime", err.Error()
}

func downtimesPath(orgID string) string {
	return fmt.Sprintf("/api/v2/%s/downtimes", pathEscape(orgID))
}

func downtimePath(orgID, id string) string {
	return downtimesPath(orgID) + "/" + pathEscape(id)
}

// CreateDowntime creates a downtime and returns its ID.
func (c *Client) CreateDowntime(ctx context.Context, orgID string, req downtimeWriteAPI) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, downtimesPath(orgID), req, &out); err != nil {
		return "", downtimeUnsupportedOr(err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("OpenObserve created the downtime but returned no id")
	}
	return out.ID, nil
}

// GetDowntime reads a downtime. It returns (nil, nil) when absent.
func (c *Client) GetDowntime(ctx context.Context, orgID, id string) (*DowntimeAPI, error) {
	var out DowntimeAPI
	if err := c.doJSON(ctx, http.MethodGet, downtimePath(orgID, id), nil, &out); err != nil {
		if isNotFound(err) && strings.Contains(strings.ToLower(err.Error()), "downtime not found") {
			return nil, nil
		}
		return nil, downtimeUnsupportedOr(err)
	}
	if out.ID == "" {
		return nil, nil
	}
	return &out, nil
}

// UpdateDowntime replaces a downtime's definition. The folder is not changed;
// MoveDowntime does that.
func (c *Client) UpdateDowntime(ctx context.Context, orgID, id string, req downtimeWriteAPI) error {
	return downtimeUnsupportedOr(c.do(ctx, http.MethodPut, downtimePath(orgID, id), req))
}

// MoveDowntime moves a downtime into another downtime folder.
func (c *Client) MoveDowntime(ctx context.Context, orgID, id, dstFolderID string) error {
	body := map[string]any{"downtime_ids": []string{id}, "dst_folder_id": dstFolderID}
	return downtimeUnsupportedOr(c.do(ctx, http.MethodPatch, downtimesPath(orgID)+"/move", body))
}

// CancelDowntime ends an active window now, or calls off a scheduled one.
func (c *Client) CancelDowntime(ctx context.Context, orgID, id string) error {
	return downtimeUnsupportedOr(c.do(ctx, http.MethodPost, downtimePath(orgID, id)+"/cancel", nil))
}

// DeleteDowntime removes a downtime that is no longer active or scheduled.
// A missing one is not an error.
func (c *Client) DeleteDowntime(ctx context.Context, orgID, id string) error {
	if err := c.do(ctx, http.MethodDelete, downtimePath(orgID, id), nil); err != nil {
		if isNotFound(err) && strings.Contains(strings.ToLower(err.Error()), "downtime not found") {
			return nil
		}
		return downtimeUnsupportedOr(err)
	}
	return nil
}
