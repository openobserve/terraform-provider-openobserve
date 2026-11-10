package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// userUpdateBody runs Update on openobserve_user from the given state and plan
// custom roles and returns the JSON body sent to PUT /api/default/users/{email}.
func userUpdateBody(t *testing.T, stateRoles, planRoles types.Set) map[string]json.RawMessage {
	t.Helper()
	ctx := context.Background()

	var mu sync.Mutex
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/default/users/jane@example.com":
			putBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"code":200,"message":"User updated successfully"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/default/users":
			_ = json.NewEncoder(w).Encode(UserListAPI{Data: []UserAPI{{Email: "jane@example.com", Role: "editor", CreatedAt: 1}}})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	r := &UserResource{client: newClient(srv.URL, "u", "p", "default")}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	s := schemaResp.Schema
	empty := tftypes.NewValue(s.Type().TerraformType(ctx), nil)

	model := func(roles types.Set) *UserResourceModel {
		return &UserResourceModel{
			ID:          types.StringValue("default/jane@example.com"),
			OrgID:       types.StringValue("default"),
			Email:       types.StringValue("jane@example.com"),
			FirstName:   types.StringValue(""),
			LastName:    types.StringValue(""),
			Role:        types.StringValue("editor"),
			CustomRoles: roles,
			Password:    types.StringNull(),
			IsExternal:  types.BoolValue(false),
			CreatedAt:   types.Int64Value(1),
		}
	}
	state := tfsdk.State{Schema: s, Raw: empty}
	if diags := state.Set(ctx, model(stateRoles)); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	plan := tfsdk.Plan{Schema: s, Raw: empty}
	if diags := plan.Set(ctx, model(planRoles)); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state.Raw.Copy()}}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatalf("decoding update body %q: %v", putBody, err)
	}
	return body
}

func rolesSet(roles ...string) types.Set {
	elems := make([]attr.Value, 0, len(roles))
	for _, r := range roles {
		elems = append(elems, types.StringValue(r))
	}
	return types.SetValueMust(types.StringType, elems)
}

func TestUserUpdateRevokesRemovedCustomRoles(t *testing.T) {
	cases := []struct {
		name        string
		state, plan types.Set
		want        string
	}{
		{"attribute removed", rolesSet("auditor"), types.SetNull(types.StringType), `[]`},
		{"set emptied", rolesSet("auditor", "oncall"), rolesSet(), `[]`},
		{"role dropped", rolesSet("auditor", "oncall"), rolesSet("oncall"), `["oncall"]`},
		// A user whose roles are granted from openobserve_role must not have
		// them revoked by an unrelated update.
		{"never managed here", types.SetNull(types.StringType), types.SetNull(types.StringType), `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := userUpdateBody(t, tc.state, tc.plan)
			got, ok := body["custom_role"]
			if !ok {
				got = json.RawMessage("null")
			}
			if string(got) != tc.want {
				t.Errorf("custom_role sent as %s, want %s", got, tc.want)
			}
		})
	}
}
