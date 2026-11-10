package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// lowercasingUserServer stores accounts the way OpenObserve does: the email of
// a new user or service account is lowercased before it is saved.
func lowercasingUserServer(t *testing.T) *Client {
	t.Helper()
	var mu sync.Mutex
	users := map[string]UserAPI{}
	accounts := map[string]UserAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		store := users
		if strings.HasPrefix(r.URL.Path, "/api/default/service_accounts") {
			store = accounts
		}
		switch r.Method {
		case http.MethodPost:
			var body struct {
				Email     string `json:"email"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Role      string `json:"role"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			email := strings.ToLower(strings.TrimSpace(body.Email))
			store[email] = UserAPI{Email: email, FirstName: body.FirstName, LastName: body.LastName, Role: body.Role, CreatedAt: 1}
			_, _ = w.Write([]byte(`{"code":200,"message":"User saved successfully","token":"t"}`))
		case http.MethodGet:
			list := []UserAPI{}
			for _, u := range store {
				list = append(list, u)
			}
			_ = json.NewEncoder(w).Encode(UserListAPI{Data: list})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "u", "p", "default")
}

func TestUserKeepsConfiguredEmailCase(t *testing.T) {
	ctx := context.Background()
	r := &UserResource{client: lowercasingUserServer(t)}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	s := schemaResp.Schema
	empty := tftypes.NewValue(s.Type().TerraformType(ctx), nil)

	plan := tfsdk.Plan{Schema: s, Raw: empty}
	if diags := plan.Set(ctx, &UserResourceModel{
		ID:          types.StringUnknown(),
		OrgID:       types.StringValue("default"),
		Email:       types.StringValue("Jane.Doe@Example.com"),
		FirstName:   types.StringValue("Jane"),
		LastName:    types.StringValue(""),
		Role:        types.StringValue("admin"),
		CustomRoles: types.SetNull(types.StringType),
		Password:    types.StringValue("Complexpass#123"),
		IsExternal:  types.BoolValue(false),
		CreatedAt:   types.Int64Unknown(),
	}); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: empty}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics)
	}
	var created UserResourceModel
	createResp.State.Get(ctx, &created)
	if got := created.Email.ValueString(); got != "Jane.Doe@Example.com" {
		t.Fatalf("email after create = %q, want the configured spelling", got)
	}

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: createResp.State.Raw.Copy()}}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics)
	}
	var read UserResourceModel
	readResp.State.Get(ctx, &read)
	if got := read.Email.ValueString(); got != "Jane.Doe@Example.com" {
		t.Errorf("email after read = %q, want the configured spelling", got)
	}
	if got := read.ID.ValueString(); got != "default/Jane.Doe@Example.com" {
		t.Errorf("id after read = %q", got)
	}

	importResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: empty}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "default/Jane.Doe@Example.com"}, importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import: %v", importResp.Diagnostics)
	}
	var imported UserResourceModel
	importResp.State.Get(ctx, &imported)
	if got := imported.Email.ValueString(); got != "Jane.Doe@Example.com" {
		t.Errorf("email after import = %q, want the spelling of the import ID", got)
	}
}

func TestServiceAccountKeepsConfiguredEmailCase(t *testing.T) {
	ctx := context.Background()
	r := &ServiceAccountResource{client: lowercasingUserServer(t)}
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	s := schemaResp.Schema
	empty := tftypes.NewValue(s.Type().TerraformType(ctx), nil)

	plan := tfsdk.Plan{Schema: s, Raw: empty}
	if diags := plan.Set(ctx, &ServiceAccountResourceModel{
		ID:          types.StringUnknown(),
		OrgID:       types.StringValue("default"),
		Email:       types.StringValue("CI-Pipeline@Example.com"),
		FirstName:   types.StringValue(""),
		LastName:    types.StringValue(""),
		CustomRoles: types.SetNull(types.StringType),
		RotateToken: types.StringNull(),
		Token:       types.StringUnknown(),
		IsSystem:    types.BoolUnknown(),
		Description: types.StringUnknown(),
		CreatedAt:   types.Int64Unknown(),
	}); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: empty}}
	r.Create(ctx, resource.CreateRequest{Plan: plan}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics)
	}
	var created ServiceAccountResourceModel
	createResp.State.Get(ctx, &created)
	if got := created.Email.ValueString(); got != "CI-Pipeline@Example.com" {
		t.Errorf("email after create = %q, want the configured spelling", got)
	}

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: createResp.State.Raw.Copy()}}
	r.Read(ctx, resource.ReadRequest{State: createResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics)
	}
	var read ServiceAccountResourceModel
	readResp.State.Get(ctx, &read)
	if got := read.Email.ValueString(); got != "CI-Pipeline@Example.com" {
		t.Errorf("email after read = %q, want the configured spelling", got)
	}
}
