package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// newRumTokenServer answers GET /api/{org}/rumtoken with body, and records
// the paths it was asked for.
func newRumTokenServer(t *testing.T, status int, body string) (*Client, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/rumtoken") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "u", "p", "default"), &paths
}

func readRumToken(t *testing.T, c *Client, orgID types.String) (RumTokenDataSourceModel, diag.Diagnostics) {
	t.Helper()
	ctx := context.Background()
	d := &RumTokenDataSource{client: c}
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	empty := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)
	cfg := tfsdk.State{Schema: schemaResp.Schema, Raw: empty}
	model := RumTokenDataSourceModel{
		ID:    types.StringNull(),
		OrgID: orgID,
		User:  types.StringNull(),
		Token: types.StringNull(),
	}
	if diags := cfg.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building config: %v", diags)
	}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: empty}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: cfg.Raw}}, resp)
	var got RumTokenDataSourceModel
	if !resp.Diagnostics.HasError() {
		if diags := resp.State.Get(ctx, &got); diags.HasError() {
			t.Fatalf("reading state: %v", diags)
		}
	}
	return got, resp.Diagnostics
}

func TestRumTokenDataSourceReadsToken(t *testing.T) {
	t.Parallel()
	c, paths := newRumTokenServer(t, http.StatusOK, `{"data":{"user":"rum@example.com","rum_token":"rum123"}}`)

	got, diags := readRumToken(t, c, types.StringValue("app"))
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if got.Token.ValueString() != "rum123" || got.User.ValueString() != "rum@example.com" {
		t.Errorf("token, user = %q, %q; want rum123, rum@example.com", got.Token.ValueString(), got.User.ValueString())
	}
	if got.ID.ValueString() != "app" || got.OrgID.ValueString() != "app" {
		t.Errorf("id, org_id = %q, %q; want app", got.ID.ValueString(), got.OrgID.ValueString())
	}
	if len(*paths) != 1 || (*paths)[0] != "GET /api/app/rumtoken" {
		t.Errorf("requests = %v, want [GET /api/app/rumtoken]", *paths)
	}
}

func TestRumTokenDataSourceDefaultsToProviderOrg(t *testing.T) {
	t.Parallel()
	c, paths := newRumTokenServer(t, http.StatusOK, `{"data":{"user":"root@example.com","rum_token":"rumroot"}}`)

	got, diags := readRumToken(t, c, types.StringNull())
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if got.OrgID.ValueString() != "default" {
		t.Errorf("org_id = %q, want the provider's default", got.OrgID.ValueString())
	}
	if len(*paths) != 1 || (*paths)[0] != "GET /api/default/rumtoken" {
		t.Errorf("requests = %v, want [GET /api/default/rumtoken]", *paths)
	}
}

func TestRumTokenSchemaMarksTokenSensitive(t *testing.T) {
	t.Parallel()
	var resp datasource.SchemaResponse
	(&RumTokenDataSource{}).Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if !resp.Schema.Attributes["token"].IsSensitive() {
		t.Error("token must be sensitive")
	}
}

func TestRumTokenDataSourceErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"null token", http.StatusOK, `{"data":{"user":"rum@example.com","rum_token":null}}`, "No RUM token"},
		{"empty token", http.StatusOK, `{"data":{"user":"rum@example.com","rum_token":""}}`, "No RUM token"},
		{"forbidden", http.StatusForbidden, `{"code":403,"message":"Unauthorized Access"}`, "Error reading RUM token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newRumTokenServer(t, tc.status, tc.body)
			_, diags := readRumToken(t, c, types.StringValue("app"))
			if !diags.HasError() {
				t.Fatal("read succeeded, want an error")
			}
			if s := diags.Errors()[0].Summary(); s != tc.want {
				t.Errorf("error summary = %q, want %q", s, tc.want)
			}
		})
	}
}
