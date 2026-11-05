package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The server stores a destination as an alert destination unless create and
// update carry module=pipeline, and a pipeline's remote_stream node cannot send
// to an alert destination.
func TestPipelineDestinationWritesSendPipelineModule(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.Method] = r.URL.Path + "?" + r.URL.RawQuery
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")
	ctx := context.Background()
	dest := PipelineDestinationAPI{Name: "remote", URL: "https://receiver.example/ingest", Method: "post"}

	if err := c.CreatePipelineDestination(ctx, "default", dest); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.UpdatePipelineDestination(ctx, "default", "remote", dest); err != nil {
		t.Fatalf("update: %v", err)
	}

	want := map[string]string{
		http.MethodPost: "/api/default/alerts/destinations?module=pipeline",
		http.MethodPut:  "/api/default/alerts/destinations/remote?module=pipeline",
	}
	for method, w := range want {
		if got[method] != w {
			t.Errorf("%s: got %q, want %q", method, got[method], w)
		}
	}
}

// The server's output_format is an externally tagged enum: three plain strings
// and two objects that carry one setting each.
var outputFormatWire = []struct {
	format, index, separator, wire string
}{
	{"json", "", "", `"json"`},
	{"ndjson", "", "", `"ndjson"`},
	{"nestedevent", "", "", `"nestedevent"`},
	{"esbulk", "logs", "", `{"esbulk":{"index":"logs"}}`},
	{"stringseparated", "", "|", `{"stringseparated":{"separator":"|"}}`},
}

// The server stores an empty index or separator without complaint. A
// destination saved that way elsewhere has to read back as "", validate, and
// encode to the same wire value, or no configuration could ever match it.
func TestPipelineDestinationEmptyOutputFormatSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		wire, format string
		index, sep   types.String
	}{
		{`{"esbulk":{"index":""}}`, "esbulk", types.StringValue(""), types.StringNull()},
		{`{"stringseparated":{"separator":""}}`, "stringseparated", types.StringNull(), types.StringValue("")},
	} {
		model := pipelineDestinationModel()
		var diags diag.Diagnostics
		outputFormatFromAPI(json.RawMessage(tc.wire), &model, &diags)
		if diags.HasError() {
			t.Fatalf("%s: %+v", tc.wire, diags)
		}
		if model.OutputFormat.ValueString() != tc.format || !model.OutputFormatIndex.Equal(tc.index) ||
			!model.OutputFormatSeparator.Equal(tc.sep) {
			t.Errorf("%s: read %s / %s / %s", tc.wire, model.OutputFormat, model.OutputFormatIndex, model.OutputFormatSeparator)
		}
		if d := validatePipelineDestination(t, model); d.HasError() {
			t.Errorf("%s: config rejected: %+v", tc.wire, d)
		}
		if d := modifyPipelineDestinationPlan(t, &model); d.HasError() {
			t.Errorf("%s: plan rejected: %+v", tc.wire, d)
		}
		if got := outputFormatToAPI(&model, &diags); string(got) != tc.wire {
			t.Errorf("%s: wrote %s", tc.wire, got)
		}
	}
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func TestPipelineDestinationOutputFormatToAPI(t *testing.T) {
	t.Parallel()
	for _, tc := range outputFormatWire {
		model := PipelineDestinationResourceModel{
			OutputFormat:          types.StringValue(tc.format),
			OutputFormatIndex:     optionalString(tc.index),
			OutputFormatSeparator: optionalString(tc.separator),
		}
		var diags diag.Diagnostics
		got := outputFormatToAPI(&model, &diags)
		if diags.HasError() {
			t.Fatalf("%s: %+v", tc.format, diags)
		}
		if string(got) != tc.wire {
			t.Errorf("%s: got %s, want %s", tc.format, got, tc.wire)
		}
	}
}

func TestPipelineDestinationOutputFormatFromAPI(t *testing.T) {
	t.Parallel()
	for _, tc := range outputFormatWire {
		var model PipelineDestinationResourceModel
		var diags diag.Diagnostics
		outputFormatFromAPI(json.RawMessage(tc.wire), &model, &diags)
		if diags.HasError() {
			t.Fatalf("%s: %+v", tc.wire, diags)
		}
		if model.OutputFormat.ValueString() != tc.format ||
			!model.OutputFormatIndex.Equal(optionalString(tc.index)) ||
			!model.OutputFormatSeparator.Equal(optionalString(tc.separator)) {
			t.Errorf("%s: got %s / %s / %s", tc.wire, model.OutputFormat, model.OutputFormatIndex, model.OutputFormatSeparator)
		}
	}
}

// A destination saved without output_format reads back without one, and the
// server sends its records as json. Reporting json keeps a destination created
// before the attribute existed from planning an update.
func TestPipelineDestinationOutputFormatAbsentReadsAsJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		var model PipelineDestinationResourceModel
		var diags diag.Diagnostics
		outputFormatFromAPI(raw, &model, &diags)
		if diags.HasError() {
			t.Fatalf("%q: %+v", raw, diags)
		}
		if model.OutputFormat.ValueString() != "json" || !model.OutputFormatIndex.IsNull() || !model.OutputFormatSeparator.IsNull() {
			t.Errorf("%q: got %s / %s / %s", raw, model.OutputFormat, model.OutputFormatIndex, model.OutputFormatSeparator)
		}
	}
}

func pipelineDestinationSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&PipelineDestinationResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %+v", resp.Diagnostics)
	}
	return resp
}

func pipelineDestinationModel() PipelineDestinationResourceModel {
	return PipelineDestinationResourceModel{
		ID:                    types.StringNull(),
		OrgID:                 types.StringValue("default"),
		Name:                  types.StringValue("remote"),
		URL:                   types.StringValue("https://receiver.example/ingest"),
		Method:                types.StringNull(),
		SkipTLSVerify:         types.BoolNull(),
		Headers:               types.MapNull(types.StringType),
		DestinationType:       types.StringNull(),
		Metadata:              types.MapNull(types.StringType),
		OutputFormat:          types.StringNull(),
		OutputFormatIndex:     types.StringNull(),
		OutputFormatSeparator: types.StringNull(),
		Module:                types.StringNull(),
	}
}

func validatePipelineDestination(t *testing.T, model PipelineDestinationResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()
	s := pipelineDestinationSchema(t).Schema
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building config: %+v", diags)
	}
	resp := &resource.ValidateConfigResponse{}
	(&PipelineDestinationResource{}).ValidateConfig(ctx,
		resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: s, Raw: plan.Raw}}, resp)
	return resp.Diagnostics
}

func TestPipelineDestinationValidateOutputFormatSettings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, format, index, separator string
		wantErr                        bool
	}{
		{"unset", "", "", "", false},
		{"ndjson", "ndjson", "", "", false},
		{"esbulk with index", "esbulk", "logs", "", false},
		{"stringseparated with separator", "stringseparated", "", "|", false},
		{"esbulk without index", "esbulk", "", "", true},
		{"stringseparated without separator", "stringseparated", "", "", true},
		{"index on json", "json", "logs", "", true},
		{"index on unset format", "", "logs", "", true},
		{"separator on esbulk", "esbulk", "logs", "|", true},
	}
	for _, tc := range cases {
		model := pipelineDestinationModel()
		model.OutputFormat = optionalString(tc.format)
		model.OutputFormatIndex = optionalString(tc.index)
		model.OutputFormatSeparator = optionalString(tc.separator)
		if diags := validatePipelineDestination(t, model); diags.HasError() != tc.wantErr {
			t.Errorf("%s: error = %v, want %v (%+v)", tc.name, diags.HasError(), tc.wantErr, diags)
		}
	}

	// Values not known until apply cannot be checked, and are not rejected.
	model := pipelineDestinationModel()
	model.OutputFormatIndex = types.StringUnknown()
	if diags := validatePipelineDestination(t, model); diags.HasError() {
		t.Errorf("unknown index: %+v", diags)
	}
	model = pipelineDestinationModel()
	model.OutputFormat = types.StringUnknown()
	model.OutputFormatSeparator = types.StringValue("|")
	if diags := validatePipelineDestination(t, model); diags.HasError() {
		t.Errorf("unknown format: %+v", diags)
	}
}

func modifyPipelineDestinationPlan(t *testing.T, model *PipelineDestinationResourceModel) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()
	s := pipelineDestinationSchema(t).Schema
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if model != nil {
		if diags := plan.Set(ctx, model); diags.HasError() {
			t.Fatalf("building plan: %+v", diags)
		}
	}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	(&PipelineDestinationResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: plan}, resp)
	return resp.Diagnostics
}

// ValidateConfig skips a value unknown until apply. Terraform plans again
// with it known before applying, and that plan has to reject a wrong
// combination, or the server drops the setting and the apply fails with
// "inconsistent result" instead of saying what is wrong.
func TestPipelineDestinationModifyPlanChecksKnownOutputFormatSettings(t *testing.T) {
	t.Parallel()
	unknown := types.StringUnknown()
	cases := []struct {
		name                     string
		format, index, separator types.String
		wantErr                  bool
	}{
		{"json", types.StringValue("json"), types.StringNull(), types.StringNull(), false},
		{"esbulk with index", types.StringValue("esbulk"), types.StringValue("logs"), types.StringNull(), false},
		{"index known on json", types.StringValue("json"), types.StringValue("logs"), types.StringNull(), true},
		{"separator known on esbulk", types.StringValue("esbulk"), types.StringValue("logs"), types.StringValue("|"), true},
		{"esbulk index known null", types.StringValue("esbulk"), types.StringNull(), types.StringNull(), true},
		{"format known, separator null", types.StringValue("stringseparated"), types.StringNull(), types.StringNull(), true},
		{"index still unknown", types.StringValue("json"), unknown, types.StringNull(), false},
		{"format still unknown", unknown, types.StringValue("logs"), types.StringNull(), false},
	}
	for _, tc := range cases {
		model := pipelineDestinationModel()
		model.OutputFormat, model.OutputFormatIndex, model.OutputFormatSeparator = tc.format, tc.index, tc.separator
		if diags := modifyPipelineDestinationPlan(t, &model); diags.HasError() != tc.wantErr {
			t.Errorf("%s: error = %v, want %v (%+v)", tc.name, diags.HasError(), tc.wantErr, diags)
		}
	}

	// Destroy has no plan to check.
	if diags := modifyPipelineDestinationPlan(t, nil); diags.HasError() {
		t.Errorf("destroy: %+v", diags)
	}
}

// Provider versions up to 1.4.2 created alert destinations. The single GET
// answers the same for both kinds, so Read checks the module=pipeline listing,
// and an alert destination reads module "alert", which the plan then converts.
func TestPipelineDestinationReadDetectsAlertDestination(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		listing, want string
	}{
		{`[{"name":"remote"}]`, "pipeline"},
		{`[{"name":"other"}]`, "alert"},
		{`[]`, "alert"},
	} {
		var listQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/default/alerts/destinations":
				listQuery = r.URL.RawQuery
				_, _ = w.Write([]byte(tc.listing))
			case "/api/default/alerts/destinations/remote":
				_, _ = w.Write([]byte(`{"name":"remote","url":"https://receiver.example/ingest","method":"post","type":"http","output_format":"ndjson","metadata":{}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		ctx := context.Background()
		s := pipelineDestinationSchema(t).Schema
		state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
		if diags := state.Set(ctx, pipelineDestinationModel()); diags.HasError() {
			t.Fatalf("building state: %+v", diags)
		}
		r := &PipelineDestinationResource{client: newClient(srv.URL, "u", "p", "default")}
		resp := &resource.ReadResponse{State: state}
		r.Read(ctx, resource.ReadRequest{State: state}, resp)
		srv.Close()
		if resp.Diagnostics.HasError() {
			t.Fatalf("read: %+v", resp.Diagnostics)
		}
		var got PipelineDestinationResourceModel
		resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
		if got.Module.ValueString() != tc.want {
			t.Errorf("listing %s: module = %s, want %s", tc.listing, got.Module, tc.want)
		}
		if got.OutputFormat.ValueString() != "ndjson" {
			t.Errorf("listing %s: output_format = %s, want ndjson", tc.listing, got.OutputFormat)
		}
		if listQuery != "module=pipeline" {
			t.Errorf("listing query = %q, want module=pipeline", listQuery)
		}
	}
}

// The modifier on its own: whatever state says, it plans "pipeline", which is
// what create and update store. The live tests cover the plan an "alert" state
// produces through Terraform.
func TestPipelineDestinationModulePlansPipeline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := pipelineDestinationSchema(t).Schema
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := plan.Set(ctx, pipelineDestinationModel()); diags.HasError() {
		t.Fatalf("building plan: %+v", diags)
	}
	for _, st := range []types.String{types.StringValue("alert"), types.StringValue("pipeline"), types.StringNull()} {
		resp := &planmodifier.StringResponse{PlanValue: st}
		destinationModuleAlwaysPipeline{}.PlanModifyString(ctx, planmodifier.StringRequest{
			Path: path.Root("module"), Plan: plan, StateValue: st, PlanValue: st, ConfigValue: types.StringNull(),
		}, resp)
		if resp.PlanValue.ValueString() != "pipeline" {
			t.Errorf("state %s: planned %s, want pipeline", st, resp.PlanValue)
		}
	}

	// Destroy: nothing to plan.
	destroy := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	resp := &planmodifier.StringResponse{PlanValue: types.StringNull()}
	destinationModuleAlwaysPipeline{}.PlanModifyString(ctx, planmodifier.StringRequest{Plan: destroy}, resp)
	if !resp.PlanValue.IsNull() {
		t.Errorf("destroy: planned %s, want null", resp.PlanValue)
	}
}

// readWithForbiddenListing runs Read against a server whose destination
// listing answers 403, and reports how many times the listing was called.
func readWithForbiddenListing(t *testing.T, priorModule types.String) (PipelineDestinationResourceModel, diag.Diagnostics, int) {
	t.Helper()
	var mu sync.Mutex
	lists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/default/alerts/destinations":
			mu.Lock()
			lists++
			mu.Unlock()
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":403,"message":"Unauthorized Access"}`))
		case "/api/default/alerts/destinations/remote":
			_, _ = w.Write([]byte(`{"name":"remote","url":"https://receiver.example/ingest","method":"post","type":"http","metadata":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	s := pipelineDestinationSchema(t).Schema
	prior := pipelineDestinationModel()
	prior.Module = priorModule
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, prior); diags.HasError() {
		t.Fatalf("building state: %+v", diags)
	}
	r := &PipelineDestinationResource{client: newClient(srv.URL, "u", "p", "default")}
	resp := &resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, resp)
	var got PipelineDestinationResourceModel
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Get(ctx, &got)...)
	}
	mu.Lock()
	defer mu.Unlock()
	return got, resp.Diagnostics, lists
}

// A role that may read the destination but not list destinations still has to
// be able to refresh it: a failed listing warns and keeps the module in state.
func TestPipelineDestinationReadKeepsModuleWhenListingFails(t *testing.T) {
	t.Parallel()
	got, diags, lists := readWithForbiddenListing(t, types.StringNull())
	if diags.HasError() {
		t.Fatalf("read failed: %+v", diags)
	}
	if lists != 1 || diags.WarningsCount() != 1 {
		t.Errorf("lists = %d, warnings = %d, want 1 and 1", lists, diags.WarningsCount())
	}
	if !got.Module.IsNull() {
		t.Errorf("module = %s, want the prior null", got.Module)
	}
	if got.OutputFormat.ValueString() != "json" {
		t.Errorf("output_format = %s, want json", got.OutputFormat)
	}
}

// Once state records pipeline, the destination was written by this version,
// and Read does not list again: a listing filtered by permissions, or served
// from a lagging cache, would otherwise plan the conversion on every refresh.
func TestPipelineDestinationReadTrustsRecordedPipelineModule(t *testing.T) {
	t.Parallel()
	got, diags, lists := readWithForbiddenListing(t, types.StringValue("pipeline"))
	if diags.HasError() || diags.WarningsCount() != 0 {
		t.Fatalf("read: %+v", diags)
	}
	if lists != 0 {
		t.Errorf("lists = %d, want 0", lists)
	}
	if got.Module.ValueString() != "pipeline" {
		t.Errorf("module = %s, want pipeline", got.Module)
	}
}

// An output_format the provider does not know the shape of is an error, not a
// silent json.
func TestPipelineDestinationOutputFormatUnexpectedShape(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`42`, `{}`, `{"a":{},"b":{}}`} {
		var model PipelineDestinationResourceModel
		var diags diag.Diagnostics
		outputFormatFromAPI(json.RawMessage(raw), &model, &diags)
		if !diags.HasError() {
			t.Errorf("%s: no error", raw)
		}
	}
}
