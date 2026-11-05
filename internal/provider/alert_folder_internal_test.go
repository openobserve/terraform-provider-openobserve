package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// rbacAlertServer emulates a folder-scoped user on a server with RBAC and
// `list_only_permitted`: the org-wide alert list is empty, and an alert shows
// up only in the `?folder=` list of the folder holding it. Folders missing
// from `listable` are hidden from the folder listing, as for a user without
// access to them.
type rbacAlertServer struct {
	mu       sync.Mutex
	folderOf map[string]string // alert ID -> folder ID
	listable []string
	// forbidden folders answer 403 to an alert list; "" is the org-wide list.
	forbidden   map[string]bool
	foldersFail bool
	lists       []string // `folder` query of every alert list call, in order
}

func (s *rbacAlertServer) move(alertID, folderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folderOf[alertID] = folderID
}

func (s *rbacAlertServer) listCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lists...)
}

func (s *rbacAlertServer) resetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists = nil
}

func newRBACAlertServer(t *testing.T, s *rbacAlertServer) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v2/default/folders/alerts":
			if s.foldersFail {
				http.Error(w, `{"code":500,"message":"boom"}`, http.StatusInternalServerError)
				return
			}
			list := []FolderAPI{}
			for _, id := range s.listable {
				list = append(list, FolderAPI{FolderID: id, Name: id})
			}
			_ = json.NewEncoder(w).Encode(FolderListAPI{List: list})
		case r.URL.Path == "/api/v2/default/alerts":
			folder := r.URL.Query().Get("folder")
			s.lists = append(s.lists, folder)
			if s.forbidden[folder] {
				http.Error(w, `{"code":403,"message":"Unauthorized Access"}`, http.StatusForbidden)
				return
			}
			list := []AlertListItemAPI{}
			for id, f := range s.folderOf {
				if folder != "" && f == folder {
					list = append(list, AlertListItemAPI{AlertID: id, FolderID: f, Name: "alert_" + id})
				}
			}
			_ = json.NewEncoder(w).Encode(AlertListAPI{List: list})
		case strings.HasPrefix(r.URL.Path, "/api/v2/default/alerts/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/v2/default/alerts/")
			if _, ok := s.folderOf[id]; !ok {
				http.Error(w, `{"code":404,"message":"not found"}`, http.StatusNotFound)
				return
			}
			// The get endpoint does not report the folder.
			_ = json.NewEncoder(w).Encode(AlertAPI{
				ID:             id,
				Name:           "alert_" + id,
				StreamType:     "logs",
				StreamName:     "app",
				QueryCondition: AlertQueryConditionAPI{QueryType: "sql", SQL: new(string)},
				Destinations:   []string{"d"},
			})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "u", "p", "default")
}

func alertSchemaState(t *testing.T) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	(&AlertResource{}).Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
}

func importAlert(t *testing.T, r *AlertResource, id string) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	resp := &resource.ImportStateResponse{State: alertSchemaState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
	return resp.State, resp.Diagnostics
}

func readAlert(t *testing.T, r *AlertResource, prior tfsdk.State) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: prior.Schema, Raw: prior.Raw.Copy()}}
	r.Read(context.Background(), resource.ReadRequest{State: prior}, resp)
	return resp.State, resp.Diagnostics
}

func stateFolder(t *testing.T, state tfsdk.State) string {
	t.Helper()
	var m AlertResourceModel
	if diags := state.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	return m.FolderID.ValueString()
}

func stateID(t *testing.T, state tfsdk.State) string {
	t.Helper()
	var m AlertResourceModel
	if diags := state.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	return m.ID.ValueString()
}

func warningSummaries(diags diag.Diagnostics) []string {
	var out []string
	for _, d := range diags.Warnings() {
		out = append(out, d.Summary())
	}
	return out
}

func TestAlertImportResolvesFolderUnderRBAC(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		importID  string
		wantLists []string
	}{
		// The two-part form starts at `default`, misses, then finds the alert
		// by scanning folders because the org-wide list is empty.
		{"org and alert", "default/A1", []string{"default", "", "team_a"}},
		// The three-part form names the folder, so one list call is enough.
		{"org, folder and alert", "default/team_a/A1", []string{"team_a"}},
		// A wrong folder in the ID only costs a scan; the server's folder wins.
		{"wrong folder", "default/team_b/A1", []string{"team_b", "", "default", "team_a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a", "team_b"}}
			r := &AlertResource{client: newRBACAlertServer(t, s)}

			state, diags := importAlert(t, r, tc.importID)
			if diags.HasError() || diags.WarningsCount() > 0 {
				t.Fatalf("import %q: %v", tc.importID, diags)
			}
			if got := stateFolder(t, state); got != "team_a" {
				t.Errorf("folder_id after import %q = %q, want team_a", tc.importID, got)
			}
			if got := stateID(t, state); got != "default/A1" {
				t.Errorf("id after import %q = %q, want default/A1", tc.importID, got)
			}
			if got := s.listCalls(); strings.Join(got, ",") != strings.Join(tc.wantLists, ",") {
				t.Errorf("list calls = %q, want %q", got, tc.wantLists)
			}

			// The refresh after import must agree, so the next plan is clean.
			s.resetCalls()
			state, diags = readAlert(t, r, state)
			if diags.HasError() || diags.WarningsCount() > 0 {
				t.Fatalf("read after import: %v", diags)
			}
			if got := stateFolder(t, state); got != "team_a" {
				t.Errorf("folder_id after read = %q, want team_a", got)
			}
			if got := s.listCalls(); len(got) != 1 || got[0] != "team_a" {
				t.Errorf("read of an unmoved alert listed %q, want only team_a", got)
			}
		})
	}
}

func TestAlertImportRejectsMalformedID(t *testing.T) {
	t.Parallel()
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a"}}
	r := &AlertResource{client: newRBACAlertServer(t, s)}
	for _, id := range []string{"A1", "default/", "/A1", "default//A1", "default/team_a/A1/x"} {
		if _, diags := importAlert(t, r, id); !diags.HasError() {
			t.Errorf("import %q: want an error", id)
		}
	}
}

func TestAlertReadDetectsFolderMove(t *testing.T) {
	t.Parallel()
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a", "team_b"}}
	r := &AlertResource{client: newRBACAlertServer(t, s)}

	state, diags := importAlert(t, r, "default/team_a/A1")
	if diags.HasError() {
		t.Fatalf("import: %v", diags)
	}

	// Someone moves the alert in the UI; the refresh must report it so the
	// plan shows the folder_id change.
	s.move("A1", "team_b")
	state, diags = readAlert(t, r, state)
	if diags.HasError() || diags.WarningsCount() > 0 {
		t.Fatalf("read after move: %v", diags)
	}
	if got := stateFolder(t, state); got != "team_b" {
		t.Errorf("folder_id after move = %q, want team_b", got)
	}
}

func TestAlertReadWarnsWhenFolderNotFound(t *testing.T) {
	t.Parallel()
	// The alert lives in a folder the caller cannot list.
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a"}}
	r := &AlertResource{client: newRBACAlertServer(t, s)}

	state, diags := importAlert(t, r, "default/team_a/A1")
	if diags.HasError() {
		t.Fatalf("import: %v", diags)
	}
	s.move("A1", "secret")
	state, diags = readAlert(t, r, state)
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if got := warningSummaries(diags); len(got) != 1 || got[0] != "Alert folder not found" {
		t.Errorf("warnings = %q, want one \"Alert folder not found\"", got)
	}
	if got := stateFolder(t, state); got != "team_a" {
		t.Errorf("folder_id = %q, want the prior team_a kept", got)
	}

	// An import of such an alert keeps the folder from the ID, with the warning.
	state, diags = importAlert(t, r, "default/A1")
	if diags.HasError() {
		t.Fatalf("import: %v", diags)
	}
	if got := warningSummaries(diags); len(got) != 1 || got[0] != "Alert folder not found" {
		t.Errorf("import warnings = %q, want one \"Alert folder not found\"", got)
	}
	if got := stateFolder(t, state); got != "default" {
		t.Errorf("folder_id after import = %q, want default", got)
	}
}

// A list the caller may not run is a miss: the scan goes on to the folders it
// can read, whether the refusal comes from the folder in state, the org-wide
// list or another folder.
func TestAlertReadToleratesForbiddenLists(t *testing.T) {
	t.Parallel()
	s := &rbacAlertServer{
		folderOf:  map[string]string{"A1": "team_b"},
		listable:  []string{"default", "team_a", "team_b"},
		forbidden: map[string]bool{"": true, "old": true, "team_a": true},
	}
	r := &AlertResource{client: newRBACAlertServer(t, s)}

	state, diags := importAlert(t, r, "default/old/A1")
	if diags.HasError() || diags.WarningsCount() > 0 {
		t.Fatalf("import: %v", diags)
	}
	if got := stateFolder(t, state); got != "team_b" {
		t.Errorf("folder_id = %q, want team_b", got)
	}
	want := []string{"old", "", "default", "team_a", "team_b"}
	if got := s.listCalls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("list calls = %q, want %q", got, want)
	}
}

// When the folders cannot be listed at all, Read keeps the folder in state
// and warns rather than failing the refresh.
func TestAlertReadWarnsWhenFolderLookupFails(t *testing.T) {
	t.Parallel()
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a", "team_b"}}
	r := &AlertResource{client: newRBACAlertServer(t, s)}

	state, diags := importAlert(t, r, "default/team_a/A1")
	if diags.HasError() {
		t.Fatalf("import: %v", diags)
	}
	s.move("A1", "team_b")
	s.mu.Lock()
	s.foldersFail = true
	s.mu.Unlock()

	state, diags = readAlert(t, r, state)
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if got := warningSummaries(diags); len(got) != 1 || got[0] != "Could not resolve alert folder" {
		t.Errorf("warnings = %q, want one \"Could not resolve alert folder\"", got)
	}
	if got := stateFolder(t, state); got != "team_a" {
		t.Errorf("folder_id = %q, want the prior team_a kept", got)
	}
}

// Without RBAC (or as root) the org-wide list carries every alert, so a moved
// alert is found without scanning the folders.
func TestFindAlertFolderUsesOrgWideListFirst(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("folder") == "" {
			_, _ = w.Write([]byte(`{"list":[{"alert_id":"A1","folder_id":"team_b","name":"a"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"list":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")

	got, err := c.FindAlertFolder(context.Background(), "default", "A1", "team_a")
	if err != nil || got != "team_b" {
		t.Fatalf("FindAlertFolder = %q, %v; want team_b", got, err)
	}
	want := []string{"/api/v2/default/alerts?folder=team_a", "/api/v2/default/alerts?"}
	if strings.Join(calls, " ") != strings.Join(want, " ") {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

// A name lookup across all folders has the same blind spot as the folder
// lookup and falls back to the same scan.
func TestFindAlertByNameScansFoldersUnderRBAC(t *testing.T) {
	t.Parallel()
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a"}}
	c := newRBACAlertServer(t, s)

	found, err := c.FindAlertByName(context.Background(), "default", "", "alert_A1")
	if err != nil || found == nil {
		t.Fatalf("FindAlertByName = %+v, %v; want the alert", found, err)
	}
	if found.AlertID != "A1" || found.FolderID != "team_a" {
		t.Errorf("found %+v, want A1 in team_a", found)
	}
}

// The get endpoint does not report the folder, so a lookup by ID used to
// leave folder_id empty.
func TestAlertDataSourceByIDResolvesFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := &rbacAlertServer{folderOf: map[string]string{"A1": "team_a"}, listable: []string{"default", "team_a"}}
	d := &AlertDataSource{client: newRBACAlertServer(t, s)}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	empty := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)
	cfg := tfsdk.State{Schema: schemaResp.Schema, Raw: empty}
	model := AlertDataSourceModel{
		AlertID:      types.StringValue("A1"),
		Destinations: types.SetNull(types.StringType),
		Tags:         types.SetNull(types.StringType),
	}
	if diags := cfg.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building config: %v", diags)
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: empty}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: cfg.Raw}}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() > 0 {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var got AlertDataSourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	if got.FolderID.ValueString() != "team_a" {
		t.Errorf("folder_id = %q, want team_a", got.FolderID.ValueString())
	}
}
