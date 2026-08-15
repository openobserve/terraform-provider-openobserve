package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---------------------------------------------------------------------------
// Folders
// ---------------------------------------------------------------------------

// FolderAPI is the wire format of a folder. The same shape is used for
// dashboard folders and alert folders; they are distinguished by the
// `folder_type` path segment.
type FolderAPI struct {
	FolderID    string `json:"folderId,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FolderListAPI wraps the folder list response.
type FolderListAPI struct {
	List []FolderAPI `json:"list"`
}

// CreateFolder creates a folder of the given type (`dashboards` or `alerts`).
func (c *Client) CreateFolder(ctx context.Context, orgID, folderType string, req FolderAPI) (*FolderAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/folders/%s", pathEscape(orgID), pathEscape(folderType))
	var out FolderAPI
	if err := c.doJSON(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetFolder reads a folder by ID. It returns (nil, nil) when absent.
func (c *Client) GetFolder(ctx context.Context, orgID, folderType, folderID string) (*FolderAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/folders/%s/%s", pathEscape(orgID), pathEscape(folderType), pathEscape(folderID))
	var out FolderAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	if out.FolderID == "" {
		out.FolderID = folderID
	}
	return &out, nil
}

// GetFolderByName resolves a folder name to a folder.
func (c *Client) GetFolderByName(ctx context.Context, orgID, folderType, name string) (*FolderAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/folders/%s/name/%s", pathEscape(orgID), pathEscape(folderType), pathEscape(name))
	var out FolderAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// UpdateFolder renames a folder or changes its description.
func (c *Client) UpdateFolder(ctx context.Context, orgID, folderType, folderID string, req FolderAPI) error {
	path := fmt.Sprintf("/api/v2/%s/folders/%s/%s", pathEscape(orgID), pathEscape(folderType), pathEscape(folderID))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteFolder removes a folder. The folder must be empty.
func (c *Client) DeleteFolder(ctx context.Context, orgID, folderType, folderID string) error {
	path := fmt.Sprintf("/api/v2/%s/folders/%s/%s", pathEscape(orgID), pathEscape(folderType), pathEscape(folderID))
	return c.deleteIgnoreMissing(ctx, path)
}

// ListFolders returns every folder of the given type.
func (c *Client) ListFolders(ctx context.Context, orgID, folderType string) ([]FolderAPI, error) {
	path := fmt.Sprintf("/api/v2/%s/folders/%s", pathEscape(orgID), pathEscape(folderType))
	var out FolderListAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// ---------------------------------------------------------------------------
// Dashboards
// ---------------------------------------------------------------------------

// DashboardAPI is the envelope OpenObserve returns for a single dashboard. The
// dashboard body itself lives under the `v1`..`v8` key matching `version`.
type DashboardAPI struct {
	V1        json.RawMessage `json:"v1,omitempty"`
	V2        json.RawMessage `json:"v2,omitempty"`
	V3        json.RawMessage `json:"v3,omitempty"`
	V4        json.RawMessage `json:"v4,omitempty"`
	V5        json.RawMessage `json:"v5,omitempty"`
	V6        json.RawMessage `json:"v6,omitempty"`
	V7        json.RawMessage `json:"v7,omitempty"`
	V8        json.RawMessage `json:"v8,omitempty"`
	Version   int             `json:"version"`
	Hash      string          `json:"hash"`
	UpdatedAt int64           `json:"updated_at,omitempty"`
}

// Body returns the versioned dashboard document carried by the envelope.
func (d *DashboardAPI) Body() json.RawMessage {
	switch d.Version {
	case 1:
		return d.V1
	case 2:
		return d.V2
	case 3:
		return d.V3
	case 4:
		return d.V4
	case 5:
		return d.V5
	case 6:
		return d.V6
	case 7:
		return d.V7
	case 8:
		return d.V8
	}
	// Fall back to whichever slot is populated, so a future version still reads.
	for _, v := range []json.RawMessage{d.V8, d.V7, d.V6, d.V5, d.V4, d.V3, d.V2, d.V1} {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// dashboardField extracts a top-level string field from the dashboard body.
func (d *DashboardAPI) dashboardField(name string) string {
	body := d.Body()
	if len(body) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	raw, ok := m[name]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// DashboardID returns the server-assigned dashboard identifier.
func (d *DashboardAPI) DashboardID() string { return d.dashboardField("dashboardId") }

// Title returns the dashboard title.
func (d *DashboardAPI) Title() string { return d.dashboardField("title") }

// Description returns the dashboard description.
func (d *DashboardAPI) Description() string { return d.dashboardField("description") }

// Owner returns the dashboard owner.
func (d *DashboardAPI) Owner() string { return d.dashboardField("owner") }

// Role returns the dashboard role.
func (d *DashboardAPI) Role() string { return d.dashboardField("role") }

// DashboardListItemAPI is one entry of the dashboards list response.
type DashboardListItemAPI struct {
	Version     int    `json:"version"`
	Hash        string `json:"hash"`
	FolderID    string `json:"folder_id"`
	FolderName  string `json:"folder_name"`
	DashboardID string `json:"dashboard_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Role        string `json:"role"`
	Owner       string `json:"owner"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// DashboardListAPI wraps the dashboards list response.
type DashboardListAPI struct {
	Dashboards []DashboardListItemAPI `json:"dashboards"`
}

// CreateDashboard stores a new dashboard document in a folder.
func (c *Client) CreateDashboard(ctx context.Context, orgID, folderID string, body json.RawMessage) (*DashboardAPI, error) {
	path := fmt.Sprintf("/api/%s/dashboards", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	var out DashboardAPI
	if err := c.doJSON(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDashboard reads a dashboard by ID. It returns (nil, nil) when absent.
func (c *Client) GetDashboard(ctx context.Context, orgID, dashboardID string) (*DashboardAPI, error) {
	path := fmt.Sprintf("/api/%s/dashboards/%s", pathEscape(orgID), pathEscape(dashboardID))
	var out DashboardAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || len(out.Body()) == 0 {
		return nil, nil
	}
	return &out, nil
}

// UpdateDashboard replaces a dashboard document. `hash` guards against
// concurrent edits and must be the value from the last read.
func (c *Client) UpdateDashboard(ctx context.Context, orgID, dashboardID, folderID, hash string, body json.RawMessage) (*DashboardAPI, error) {
	path := fmt.Sprintf("/api/%s/dashboards/%s?folder=%s", pathEscape(orgID), pathEscape(dashboardID), pathEscape(folderID))
	if hash != "" {
		path += "&hash=" + pathEscape(hash)
	}
	var out DashboardAPI
	if err := c.doJSON(ctx, http.MethodPut, path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDashboard removes a dashboard.
func (c *Client) DeleteDashboard(ctx context.Context, orgID, dashboardID, folderID string) error {
	path := fmt.Sprintf("/api/%s/dashboards/%s", pathEscape(orgID), pathEscape(dashboardID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	return c.deleteIgnoreMissing(ctx, path)
}

// MoveDashboard relocates a dashboard between folders.
func (c *Client) MoveDashboard(ctx context.Context, orgID, dashboardID, from, to string) error {
	path := fmt.Sprintf("/api/%s/folders/dashboards/%s", pathEscape(orgID), pathEscape(dashboardID))
	return c.do(ctx, http.MethodPut, path, map[string]string{"from": from, "to": to})
}

// FindDashboardFolder resolves which folder holds a dashboard.
//
// The list endpoint only searches the `default` folder when no folder is given,
// so each folder is checked in turn. It returns "" when the dashboard is not
// found in any folder.
func (c *Client) FindDashboardFolder(ctx context.Context, orgID, dashboardID string) (string, error) {
	folders, err := c.ListFolders(ctx, orgID, "dashboards")
	if err != nil {
		return "", err
	}
	for _, folder := range folders {
		dashboards, err := c.ListDashboards(ctx, orgID, folder.FolderID)
		if err != nil {
			return "", err
		}
		for _, d := range dashboards {
			if d.DashboardID == dashboardID {
				return folder.FolderID, nil
			}
		}
	}
	return "", nil
}

// FindDashboardByTitle resolves a dashboard title to its list entry, searching
// every folder when folderID is empty.
func (c *Client) FindDashboardByTitle(ctx context.Context, orgID, folderID, title string) (*DashboardListItemAPI, error) {
	folderIDs := []string{folderID}
	if folderID == "" {
		folders, err := c.ListFolders(ctx, orgID, "dashboards")
		if err != nil {
			return nil, err
		}
		folderIDs = folderIDs[:0]
		for _, f := range folders {
			folderIDs = append(folderIDs, f.FolderID)
		}
	}

	for _, id := range folderIDs {
		dashboards, err := c.ListDashboards(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		for i := range dashboards {
			if dashboards[i].Title == title {
				return &dashboards[i], nil
			}
		}
	}
	return nil, nil
}

// ListDashboards returns the dashboards of an organization, optionally scoped
// to one folder.
func (c *Client) ListDashboards(ctx context.Context, orgID, folderID string) ([]DashboardListItemAPI, error) {
	path := fmt.Sprintf("/api/%s/dashboards", pathEscape(orgID))
	if folderID != "" {
		path += "?folder=" + pathEscape(folderID)
	}
	var out DashboardListAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Dashboards, nil
}
