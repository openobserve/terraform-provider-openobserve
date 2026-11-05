package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// versionedPipelineServer stores one pipeline and applies OpenObserve's
// optimistic lock: an update is refused with 409 unless it carries the stored
// version, and an accepted update bumps it.
func versionedPipelineServer(t *testing.T, stored PipelineAPI) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(stored)
		case http.MethodPut:
			var body PipelineAPI
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.Version != stored.Version {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":409,"message":"Pipeline with ID p1 modified by someone else. Please refresh."}`))
				return
			}
			body.Version++
			stored = body
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pipelineModel(description string, version types.Int64) PipelineResourceModel {
	return PipelineResourceModel{
		ID:          types.StringValue("default/p1"),
		PipelineID:  types.StringValue("p1"),
		OrgID:       types.StringValue("default"),
		Name:        types.StringValue("repro"),
		Description: types.StringValue(description),
		Enabled:     types.BoolValue(true),
		SourceType:  types.StringValue("realtime"),
		StreamName:  types.StringValue("in"),
		StreamType:  types.StringValue("logs"),
		Nodes: []PipelineNodeModel{
			{ID: types.StringValue("source"), Type: types.StringValue("stream"), StreamName: types.StringValue("in")},
			{ID: types.StringValue("destination"), Type: types.StringValue("stream"), StreamName: types.StringValue("out")},
		},
		Edges:   []PipelineEdgeModel{{From: types.StringValue("source"), To: types.StringValue("destination")}},
		Version: version,
	}
}

// applyPipelineUpdate runs Update from prior state to a plan with a new
// description, the way Terraform does: the computed version is unknown in the
// plan.
func applyPipelineUpdate(t *testing.T, r *PipelineResource, prior tfsdk.State, description string) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	plan := tfsdk.Plan{Schema: prior.Schema, Raw: tftypes.NewValue(prior.Schema.Type().TerraformType(ctx), nil)}
	if diags := plan.Set(ctx, pipelineModel(description, types.Int64Unknown())); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: prior.Schema, Raw: prior.Raw.Copy()}}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: prior}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update to %q: %v", description, resp.Diagnostics)
	}
	return resp.State
}

func TestPipelineUpdateSendsStoredVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &PipelineResource{}

	stored := pipelineModel("a", types.Int64Value(0))
	srv := versionedPipelineServer(t, r.apiFromModel(&stored, "default"))
	r.client = newClient(srv.URL, "u", "p", "default")

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, stored); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}

	// The first update passed even without the version, because the server
	// defaults a missing version to 0. Each one after that has to send it.
	for i, description := range []string{"b", "c", "d"} {
		state = applyPipelineUpdate(t, r, state, description)

		var got PipelineResourceModel
		if diags := state.Get(ctx, &got); diags.HasError() {
			t.Fatalf("reading state: %v", diags)
		}
		if want := int64(i + 1); got.Version.ValueInt64() != want {
			t.Fatalf("version after update %d: got %s, want %d", i+1, got.Version, want)
		}
		if got.Description.ValueString() != description {
			t.Fatalf("description: got %s, want %q", got.Description, description)
		}
	}
}
