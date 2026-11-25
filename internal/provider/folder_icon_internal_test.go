package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const testFolderPath = "/api/v2/default/folders/dashboards/f1"

func folderModel(name string, icon types.String) FolderResourceModel {
	return FolderResourceModel{
		ID:          types.StringValue("default/dashboards/f1"),
		OrgID:       types.StringValue("default"),
		FolderID:    types.StringValue("f1"),
		FolderType:  types.StringValue("dashboards"),
		Name:        types.StringValue(name),
		Description: types.StringValue(""),
		Icon:        icon,
	}
}

func storedFolder(icon any) map[string]any {
	f := map[string]any{"folderId": "f1", "name": "before", "description": ""}
	if icon != nil {
		f["icon"] = icon
	}
	return f
}

// runFolderUpdate renames the folder against a server holding serverIcon.
func runFolderUpdate(t *testing.T, serverIcon any, planned, prior, configured types.String) (*replacingServer, FolderResourceModel) {
	t.Helper()
	fake, srv := newReplacingServer(t)
	fake.put(testFolderPath, storedFolder(serverIcon))
	r := &FolderResource{client: newClient(srv.URL, "u", "p", "default")}
	req, resp := updateRequest(t, r, folderModel("after", planned), folderModel("before", prior), folderModel("after", configured))
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var got FolderResourceModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("decode state: %v", d)
	}
	return fake, got
}

// An unset icon is read from the server and sent back, so an icon picked in
// the UI survives a rename, even when state does not know it yet.
func TestFolderUpdateKeepsUnsetIcon(t *testing.T) {
	t.Parallel()
	fake, got := runFolderUpdate(t, "🚀", types.StringNull(), types.StringNull(), types.StringNull())
	if fake.lastPut["icon"] != "🚀" {
		t.Errorf("PUT icon: got %v, want the server's", fake.lastPut["icon"])
	}
	if got.Name.ValueString() != "after" {
		t.Errorf("name: got %v", got.Name)
	}
}

// A configured icon is sent as written, and "" removes it: the field is left
// out of the PUT and state keeps "".
func TestFolderUpdateConfiguredIcon(t *testing.T) {
	t.Parallel()
	fake, got := runFolderUpdate(t, "🚀", types.StringValue("o2:redis"), types.StringValue("🚀"), types.StringValue("o2:redis"))
	if fake.lastPut["icon"] != "o2:redis" || got.Icon.ValueString() != "o2:redis" {
		t.Errorf("set: PUT %v, state %v", fake.lastPut["icon"], got.Icon)
	}
	fake, got = runFolderUpdate(t, "🚀", types.StringValue(""), types.StringValue("🚀"), types.StringValue(""))
	if _, ok := fake.lastPut["icon"]; ok {
		t.Errorf("clear: PUT icon %v, want the field left out", fake.lastPut["icon"])
	}
	if got.Icon.IsNull() || got.Icon.ValueString() != "" {
		t.Errorf("clear: state %v, want \"\"", got.Icon)
	}
}

// Read reports the server's icon, keeps a configured "" when the folder has
// none, and reads an absent icon as null on import.
func TestFolderReadIcon(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		server any
		prior  types.String
		want   types.String
	}{
		{"set", "🚀", types.StringNull(), types.StringValue("🚀")},
		{"none", nil, types.StringNull(), types.StringNull()},
		{"cleared", nil, types.StringValue(""), types.StringValue("")},
		{"changed in the UI", "o2:redis", types.StringValue("🚀"), types.StringValue("o2:redis")},
	} {
		fake, srv := newReplacingServer(t)
		fake.put(testFolderPath, storedFolder(tc.server))
		r := &FolderResource{client: newClient(srv.URL, "u", "p", "default")}
		st := tfValue(t, r, folderModel("before", tc.prior))
		resp := &resource.ReadResponse{State: st}
		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
		var got FolderResourceModel
		if d := resp.State.Get(context.Background(), &got); d.HasError() {
			t.Fatalf("%s: %v %v", tc.name, resp.Diagnostics, d)
		}
		if !got.Icon.Equal(tc.want) {
			t.Errorf("%s: icon %v, want %v", tc.name, got.Icon, tc.want)
		}
	}
}

// Create sends a configured icon and resolves an unset one to the server's.
func TestFolderCreateIcon(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	r := &FolderResource{client: newClient(srv.URL, "u", "p", "default")}
	plan := folderModel("before", types.StringValue("🚀"))
	plan.ID, plan.FolderID = types.StringUnknown(), types.StringUnknown()
	p := tfValue(t, r, plan)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(p), Config: tfsdk.Config(p)}, resp)
	created := fake.get("/api/v2/default/folders/dashboards/before")
	if created == nil || created["icon"] != "🚀" {
		t.Errorf("POST body: %v", created)
	}
}
