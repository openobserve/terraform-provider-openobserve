package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// anomalyModel is a minimal valid configuration: a count over a whole stream,
// with every optional value at its schema default.
func anomalyModel() AnomalyAlertResourceModel {
	m := emptyAnomalyModel()
	m.ID = types.StringNull()
	m.AnomalyID = types.StringNull()
	m.OrgID = types.StringNull()
	m.FolderID = types.StringValue("default")
	m.Name = types.StringValue("checkout_volume")
	m.Description = types.StringValue("")
	m.StreamName = types.StringValue("app_logs")
	m.StreamType = types.StringValue("logs")
	m.CustomSQL = types.StringNull()
	m.DetectionFunction = types.StringValue("count")
	m.DetectionFunctionField = types.StringNull()
	m.HistogramInterval = types.StringValue("5m")
	m.ScheduleInterval = types.StringValue("15m")
	m.DetectionWindowSeconds = types.Int64Value(3600)
	m.TrainingWindowDays = types.Int64Value(28)
	m.RetrainIntervalDays = types.Int64Value(7)
	m.AlertBudgetPerDay = types.Float64Null()
	m.BandWidth = types.Float64Null()
	m.AlertDirection = types.StringValue("both")
	m.AlertWindowBuckets = types.Int64Value(1)
	m.AlertWindowFirePct = types.Float64Value(100)
	m.AlertWindowRecoverPct = types.Float64Null()
	m.AlertEnabled = types.BoolValue(true)
	m.Enabled = types.BoolValue(true)
	m.Owner = types.StringNull()
	m.Priority = types.Int64Null()
	m.BandGrouping = types.StringNull()
	m.BandK = types.Float64Null()
	m.TrainingDataStartUs = types.Int64Null()
	m.TrainingDataEndUs = types.Int64Null()
	m.IsTrained = types.BoolNull()
	m.Status = types.StringNull()
	return m
}

func anomalySchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	(&AnomalyAlertResource{}).Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %+v", resp.Diagnostics)
	}
	return resp.Schema
}

// anomalyConfig turns a model into a tfsdk.Config for the anomaly schema.
func anomalyConfig(t *testing.T, model AnomalyAlertResourceModel) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	s := anomalySchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building config: %+v", diags)
	}
	return tfsdk.Config{Schema: s, Raw: plan.Raw}
}

func validateAnomaly(t *testing.T, model AnomalyAlertResourceModel) diag.Diagnostics {
	t.Helper()
	resp := &resource.ValidateConfigResponse{}
	(&AnomalyAlertResource{}).ValidateConfig(context.Background(),
		resource.ValidateConfigRequest{Config: anomalyConfig(t, model)}, resp)
	return resp.Diagnostics
}

// float64AttrDiags runs the schema validators of one float attribute, which
// is where the range and conflict rules on band_width live.
func float64AttrDiags(t *testing.T, model AnomalyAlertResourceModel, name string, value types.Float64) diag.Diagnostics {
	t.Helper()
	attribute, ok := anomalySchema(t).Attributes[name].(schema.Float64Attribute)
	if !ok {
		t.Fatalf("%s is not a float attribute", name)
	}
	req := validator.Float64Request{
		Path:           path.Root(name),
		PathExpression: path.MatchRoot(name),
		Config:         anomalyConfig(t, model),
		ConfigValue:    value,
	}
	var diags diag.Diagnostics
	for _, v := range attribute.Validators {
		resp := &validator.Float64Response{}
		v.ValidateFloat64(context.Background(), req, resp)
		diags.Append(resp.Diagnostics...)
	}
	return diags
}

func diagSummaries(diags diag.Diagnostics) string {
	var out []string
	for _, d := range diags {
		out = append(out, d.Summary()+": "+d.Detail())
	}
	return strings.Join(out, "\n")
}

func TestAnomalyMinimalConfigIsValid(t *testing.T) {
	t.Parallel()
	if diags := validateAnomaly(t, anomalyModel()); diags.HasError() {
		t.Fatalf("unexpected diagnostics:\n%s", diagSummaries(diags))
	}
}

func TestAnomalyBandWidthConflictsWithBudget(t *testing.T) {
	t.Parallel()

	m := anomalyModel()
	m.BandWidth = types.Float64Value(4)
	m.AlertBudgetPerDay = types.Float64Value(5)
	diags := float64AttrDiags(t, m, "band_width", m.BandWidth)
	if !diags.HasError() || !strings.Contains(diagSummaries(diags), "alert_budget_per_day") {
		t.Fatalf("band_width with alert_budget_per_day was not rejected:\n%s", diagSummaries(diags))
	}

	m.AlertBudgetPerDay = types.Float64Null()
	if diags := float64AttrDiags(t, m, "band_width", m.BandWidth); diags.HasError() {
		t.Fatalf("band_width alone was rejected:\n%s", diagSummaries(diags))
	}
}

func TestAnomalyBandWidthRange(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value float64
		ok    bool
	}{{0.5, false}, {1, true}, {4.5, true}, {10, true}, {10.5, false}} {
		m := anomalyModel()
		m.BandWidth = types.Float64Value(tc.value)
		diags := float64AttrDiags(t, m, "band_width", m.BandWidth)
		if diags.HasError() == tc.ok {
			t.Errorf("band_width = %v: ok = %v, diagnostics:\n%s", tc.value, !diags.HasError(), diagSummaries(diags))
		}
	}
}

func TestAnomalyValidateConfigRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*AnomalyAlertResourceModel)
		want   string // substring of the error; empty means valid
	}{
		{"budget zero", func(m *AnomalyAlertResourceModel) { m.AlertBudgetPerDay = types.Float64Value(0) }, "Invalid alert budget"},
		{"budget positive", func(m *AnomalyAlertResourceModel) { m.AlertBudgetPerDay = types.Float64Value(0.5) }, ""},
		{"fire zero", func(m *AnomalyAlertResourceModel) { m.AlertWindowFirePct = types.Float64Value(0) }, "fire percent"},
		{"fire above 100", func(m *AnomalyAlertResourceModel) { m.AlertWindowFirePct = types.Float64Value(101) }, "fire percent"},
		{"recover above fire", func(m *AnomalyAlertResourceModel) {
			m.AlertWindowFirePct = types.Float64Value(60)
			m.AlertWindowRecoverPct = types.Float64Value(70)
		}, "Recover percent above fire percent"},
		{"recover equal to fire", func(m *AnomalyAlertResourceModel) {
			m.AlertWindowFirePct = types.Float64Value(60)
			m.AlertWindowRecoverPct = types.Float64Value(60)
		}, ""},
		{"recover against default fire", func(m *AnomalyAlertResourceModel) {
			m.AlertWindowFirePct = types.Float64Null()
			m.AlertWindowRecoverPct = types.Float64Value(100)
		}, ""},
		{"recover zero", func(m *AnomalyAlertResourceModel) { m.AlertWindowRecoverPct = types.Float64Value(0) }, "recover percent"},
		{"recover with unknown fire", func(m *AnomalyAlertResourceModel) {
			m.AlertWindowFirePct = types.Float64Unknown()
			m.AlertWindowRecoverPct = types.Float64Value(90)
		}, ""},
		{"count with a field", func(m *AnomalyAlertResourceModel) { m.DetectionFunctionField = types.StringValue("latency") }, "not allowed with count"},
		{"avg without a field", func(m *AnomalyAlertResourceModel) { m.DetectionFunction = types.StringValue("avg") }, "Missing detection_function_field"},
		{"avg with a field", func(m *AnomalyAlertResourceModel) {
			m.DetectionFunction = types.StringValue("avg")
			m.DetectionFunctionField = types.StringValue("latency")
		}, ""},
		{"unknown field", func(m *AnomalyAlertResourceModel) {
			m.DetectionFunction = types.StringValue("avg")
			m.DetectionFunctionField = types.StringUnknown()
		}, ""},
		{"uppercase tag", func(m *AnomalyAlertResourceModel) {
			m.Tags = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("Prod")})
		}, "lowercase"},
		{"lowercase tag", func(m *AnomalyAlertResourceModel) {
			m.Tags = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("team:checkout")})
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := anomalyModel()
			tc.mutate(&m)
			diags := validateAnomaly(t, m)
			got := diagSummaries(diags)
			switch {
			case tc.want == "" && diags.HasError():
				t.Fatalf("unexpected diagnostics:\n%s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Fatalf("want an error containing %q, got:\n%s", tc.want, got)
			}
		})
	}
}

func TestSplitDetectionFunction(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in, fn, field string
	}{
		{"count(*)", "count", ""},
		{"count", "count", ""},
		{"avg(latency_ms)", "avg", "latency_ms"},
		{" p95( duration ) ", "p95", "duration"},
	} {
		fn, field := splitDetectionFunction(tc.in)
		gotField := ""
		if field != nil {
			gotField = *field
		}
		if fn != tc.fn || gotField != tc.field {
			t.Errorf("splitDetectionFunction(%q) = %q, %q; want %q, %q", tc.in, fn, gotField, tc.fn, tc.field)
		}
	}
}

// TestAnomalyWriteBodyClearsUnsetSettings checks the update body sends every
// band setting, as null when unset: the server keeps a stored value for an
// absent field, so leaving them out would make removing one a silent no-op.
func TestAnomalyWriteBodyClearsUnsetSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A plan carries the schema defaults for direction, buckets and fire
	// percent; the attributes without a default are null when unset.
	m := anomalyModel()
	var diags diag.Diagnostics
	body := anomalyWriteFromModel(ctx, &m, false, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %+v", diags)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"band_width", "alert_budget_per_day", "alert_window_recover_pct", "priority", "detection_function_field",
	} {
		if v, ok := got[key]; !ok || string(v) != "null" {
			t.Errorf("%s = %s (present %v), want an explicit null", key, v, ok)
		}
	}
	for key, want := range map[string]string{
		"alert_direction": `"both"`, "alert_window_buckets": "1", "alert_window_fire_pct": "100",
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want the default %s", key, got[key], want)
		}
	}

	// An unchanged schedule_interval is left out, since sending it makes the
	// server reset the detection trigger.
	body.ScheduleInterval = ""
	encoded, _ = json.Marshal(body)
	if strings.Contains(string(encoded), "schedule_interval") {
		t.Error("an empty schedule_interval is still sent")
	}
	for _, key := range []string{"tags", "alert_destinations", "filters"} {
		if string(got[key]) != "[]" {
			t.Errorf("%s = %s, want []", key, got[key])
		}
	}
	if string(got["query_mode"]) != `"filters"` {
		t.Errorf("query_mode = %s, want filters", got["query_mode"])
	}
	// An update cannot move a config to another stream, so it never sends one.
	if _, ok := got["stream_name"]; ok {
		t.Error("update body carries stream_name")
	}
	// The ignored and legacy fields are never sent.
	for _, key := range []string{"seasonality", "rcf_num_trees", "rcf_tree_size", "rcf_shingle_size",
		"level_half_width_seconds", "threshold", "percentile"} {
		if _, ok := got[key]; ok {
			t.Errorf("body carries %s", key)
		}
	}
}

func TestAnomalyWriteBodyCustomSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	m := anomalyModel()
	m.CustomSQL = types.StringValue(`SELECT histogram(_timestamp, '5m') AS _timestamp, count(*) AS value FROM "app_logs" GROUP BY _timestamp`)
	m.BandWidth = types.Float64Value(4.5)
	var diags diag.Diagnostics
	body := anomalyWriteFromModel(ctx, &m, true, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %+v", diags)
	}
	if body.QueryMode != "custom_sql" || body.CustomSQL == nil || body.Filters != nil {
		t.Errorf("custom SQL body: mode %q, sql %v, filters %v", body.QueryMode, body.CustomSQL, body.Filters)
	}
	if body.StreamName != "app_logs" || body.StreamType != "logs" {
		t.Errorf("create body stream = %q/%q", body.StreamType, body.StreamName)
	}
	if body.BandWidth == nil || *body.BandWidth != 4.5 {
		t.Errorf("band_width = %v", body.BandWidth)
	}
}

func TestAnomalyWriteBodyFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	m := anomalyModel()
	elemType := types.ObjectType{AttrTypes: anomalyFilterAttrTypes}
	m.Filters = types.ListValueMust(elemType, []attr.Value{
		types.ObjectValueMust(anomalyFilterAttrTypes, map[string]attr.Value{
			"field": types.StringValue("service"), "operator": types.StringValue("="), "value": types.StringValue("checkout"),
		}),
	})
	var diags diag.Diagnostics
	body := anomalyWriteFromModel(ctx, &m, true, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %+v", diags)
	}
	if body.Filters == nil || len(*body.Filters) != 1 || (*body.Filters)[0].Value != "checkout" {
		t.Fatalf("filters = %+v", body.Filters)
	}
}

// TestApplyAnomalyToModel reads back a config as the server stores it. NULL
// band settings mean the defaults, which state holds as values so that a
// configuration leaving them out plans clean.
func TestApplyAnomalyToModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	raw := json.RawMessage(`{
		"anomaly_id": "3Lh", "org_id": "default", "name": "n", "description": null,
		"stream_name": "app_logs", "stream_type": "logs", "query_mode": "filters", "filters": [],
		"custom_sql": null, "detection_function": "p95(duration)", "histogram_interval": "5m",
		"schedule_interval": "15m", "detection_window_seconds": 3600, "training_window_days": 28,
		"retrain_interval_days": 7, "alert_budget_per_day": null, "band_width": null,
		"alert_direction": null, "alert_window_buckets": null, "alert_window_fire_pct": null,
		"alert_window_recover_pct": null, "alert_enabled": true, "alert_destinations": [],
		"enabled": true, "folder_id": "default", "owner": "a@example.com", "priority": null, "tags": null,
		"band_grouping": "weekend_hour", "band_k": 4.25, "training_data_start_us": 100,
		"training_data_end_us": 200, "is_trained": true, "status": "ready",
		"seasonality": "none", "rcf_num_trees": 100, "threshold": 97
	}`)
	cfg, err := decodeAnomalyConfig(raw)
	if err != nil || cfg == nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.HasBandSettings {
		t.Error("a server with the band columns was reported as too old")
	}

	m := emptyAnomalyModel()
	var diags diag.Diagnostics
	applyAnomalyToModel(ctx, cfg, &m, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %+v", diags)
	}
	checks := map[string][2]any{
		"description":              {m.Description, types.StringValue("")},
		"detection_function":       {m.DetectionFunction, types.StringValue("p95")},
		"detection_function_field": {m.DetectionFunctionField, types.StringValue("duration")},
		"alert_direction":          {m.AlertDirection, types.StringValue("both")},
		"alert_window_buckets":     {m.AlertWindowBuckets, types.Int64Value(1)},
		"alert_window_fire_pct":    {m.AlertWindowFirePct, types.Float64Value(100)},
		"alert_window_recover_pct": {m.AlertWindowRecoverPct, types.Float64Null()},
		"band_width":               {m.BandWidth, types.Float64Null()},
		"band_grouping":            {m.BandGrouping, types.StringValue("weekend_hour")},
		"band_k":                   {m.BandK, types.Float64Value(4.25)},
		"training_data_start_us":   {m.TrainingDataStartUs, types.Int64Value(100)},
		"training_data_end_us":     {m.TrainingDataEndUs, types.Int64Value(200)},
		"status":                   {m.Status, types.StringValue("ready")},
		"custom_sql":               {m.CustomSQL, types.StringNull()},
		"filters":                  {m.Filters, types.ListNull(types.ObjectType{AttrTypes: anomalyFilterAttrTypes})},
		"alert_destinations":       {m.AlertDestinations, types.SetNull(types.StringType)},
		"tags":                     {m.Tags, types.SetNull(types.StringType)},
		"priority":                 {m.Priority, types.Int64Null()},
	}
	for name, pair := range checks {
		got, want := pair[0].(attr.Value), pair[1].(attr.Value)
		if !got.Equal(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestDecodeAnomalyConfigFromOlderServer(t *testing.T) {
	t.Parallel()

	cfg, err := decodeAnomalyConfig(json.RawMessage(`{"anomaly_id":"x","name":"n","threshold":97}`))
	if err != nil || cfg == nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.HasBandSettings {
		t.Fatal("a response without the band columns was reported as current")
	}
	var diags diag.Diagnostics
	if requireBandSettings(cfg, &diags) || !strings.Contains(diagSummaries(diags), anomalyMinVersion) {
		t.Fatalf("older server not reported with the minimum version: %s", diagSummaries(diags))
	}
}

// TestAnomalyClientAgainstServer drives the client against a fake server: the
// create body, the read-back, and a missing config.
func TestAnomalyClientAgainstServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/default/anomaly_detection":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			_, _ = w.Write([]byte(`{"anomaly_id":"abc","status":0}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/default/anomaly_detection/abc":
			_, _ = w.Write([]byte(`{"anomaly_id":"abc","name":"n","band_width":4,"alert_direction":"above",` +
				`"alert_window_buckets":3,"alert_window_fire_pct":60,"alert_window_recover_pct":40,"status":"training"}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"Anomaly config not found: gone"}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")

	m := anomalyModel()
	m.BandWidth = types.Float64Value(4)
	var diags diag.Diagnostics
	id, err := c.CreateAnomalyConfig(ctx, "default", anomalyWriteFromModel(ctx, &m, true, &diags))
	if err != nil || id != "abc" {
		t.Fatalf("CreateAnomalyConfig = %q, %v", id, err)
	}
	if created["band_width"] != 4.0 || created["query_mode"] != "filters" || created["stream_name"] != "app_logs" {
		t.Errorf("create body = %v", created)
	}

	cfg, err := c.GetAnomalyConfig(ctx, "default", "abc")
	if err != nil || cfg == nil {
		t.Fatalf("GetAnomalyConfig: %v", err)
	}
	if cfg.BandWidth == nil || *cfg.BandWidth != 4 || cfg.AlertWindowRecoverPct == nil || *cfg.AlertWindowRecoverPct != 40 {
		t.Errorf("read back %+v", cfg)
	}

	gone, err := c.GetAnomalyConfig(ctx, "default", "gone")
	if err != nil || gone != nil {
		t.Errorf("missing config = %+v, %v; want nil, nil", gone, err)
	}
}

// TestAnomalyCreateOnOpenSource checks the open-source answer, a 404 for a
// route it does not have, becomes a diagnostic naming the missing edition.
// orgNotFoundServer answers every API path as both editions answer a path
// they cannot serve, 404 "Organization not found", and /config with the
// given edition.
func orgNotFoundServer(t *testing.T, buildType string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/config" {
			_, _ = w.Write([]byte(`{"build_type":"` + buildType + `"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Organization not found"))
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "u", "p", "default")
}

func TestAnomalyCreateOnOpenSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := orgNotFoundServer(t, "opensource")

	m := anomalyModel()
	var diags diag.Diagnostics
	_, err := c.CreateAnomalyConfig(ctx, "default", anomalyWriteFromModel(ctx, &m, true, &diags))
	if !errors.Is(err, errAnomalyUnsupported) {
		t.Fatalf("err = %v, want errAnomalyUnsupported", err)
	}
	summary, detail := anomalyErrorDiag("creating", err)
	if !strings.Contains(summary, "Enterprise") || !strings.Contains(detail, anomalyMinVersion) {
		t.Errorf("diagnostic = %q / %q", summary, detail)
	}

	// A refresh or import must say the same, not drop the resource as missing.
	if got, err := c.GetAnomalyConfig(ctx, "default", "abc"); !errors.Is(err, errAnomalyUnsupported) {
		t.Errorf("GetAnomalyConfig on open source = %+v, %v; want errAnomalyUnsupported", got, err)
	}
}

func TestAnomalyDisabledServerDiagnostic(t *testing.T) {
	t.Parallel()

	err := &apiError{StatusCode: http.StatusForbidden, Body: `{"code":403,"message":"Anomaly detection is disabled"}`}
	summary, _ := anomalyErrorDiag("creating", err)
	if !strings.Contains(summary, "disabled") {
		t.Errorf("summary = %q", summary)
	}
}

// TestAnomalyDeletedBehindRBAC covers a server enforcing RBAC, which answers
// 403 rather than 404 for a deleted config. The list decides whether the
// config is gone or the caller really lacks permission.
func TestAnomalyDeletedBehindRBAC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/default/anomaly_detection" {
			_, _ = w.Write([]byte(`[{"anomaly_id":"present","name":"p"}]`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Unauthorized Access"))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")

	if got, err := c.GetAnomalyConfig(ctx, "default", "deleted"); err != nil || got != nil {
		t.Errorf("deleted config = %+v, %v; want nil, nil", got, err)
	}
	if err := c.DeleteAnomalyConfig(ctx, "default", "deleted"); err != nil {
		t.Errorf("deleting a deleted config: %v", err)
	}
	if _, err := c.GetAnomalyConfig(ctx, "default", "present"); err == nil {
		t.Error("a 403 on a config that still exists was swallowed")
	}
	if err := c.DeleteAnomalyConfig(ctx, "default", "present"); err == nil {
		t.Error("a 403 deleting a config that still exists was swallowed")
	}
}

// TestAnomalyMissingOrganizationOnEnterprise checks that an Enterprise
// server's 404 for an organization that does not exist is not reported as a
// missing edition: create shows the server's answer, and a read treats the
// alert as gone.
func TestAnomalyMissingOrganizationOnEnterprise(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := orgNotFoundServer(t, "enterprise")

	m := anomalyModel()
	var diags diag.Diagnostics
	_, err := c.CreateAnomalyConfig(ctx, "nope", anomalyWriteFromModel(ctx, &m, true, &diags))
	if err == nil || errors.Is(err, errAnomalyUnsupported) || !strings.Contains(err.Error(), "Organization not found") {
		t.Fatalf("create in a missing organization: %v", err)
	}
	summary, _ := anomalyErrorDiag("creating", err)
	if strings.Contains(summary, "Enterprise") {
		t.Errorf("missing organization reported as %q", summary)
	}
	if got, err := c.GetAnomalyConfig(ctx, "nope", "abc"); err != nil || got != nil {
		t.Errorf("read in a missing organization = %+v, %v; want nil, nil", got, err)
	}
	if _, _, err := c.AnomalyBandSettingsSupported(ctx, "nope"); errors.Is(err, errAnomalyUnsupported) {
		t.Errorf("band check in a missing organization reported a missing edition: %v", err)
	}
}

func TestAnomalyBandSettingsSupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, tc := range []struct {
		name, body       string
		known, supported bool
	}{
		{"no configs", `[]`, false, false},
		{"current server", `[{"anomaly_id":"a","alert_window_fire_pct":null}]`, true, true},
		{"older server", `[{"anomaly_id":"a","threshold":97}]`, true, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		}))
		c := newClient(srv.URL, "u", "p", "default")
		known, supported, err := c.AnomalyBandSettingsSupported(ctx, "default")
		srv.Close()
		if err != nil || known != tc.known || supported != tc.supported {
			t.Errorf("%s: known %v, supported %v, err %v; want %v, %v", tc.name, known, supported, err, tc.known, tc.supported)
		}
	}
}

func TestAnomalyDetectionFieldWhitespace(t *testing.T) {
	t.Parallel()

	m := anomalyModel()
	m.DetectionFunction = types.StringValue("avg")
	m.DetectionFunctionField = types.StringValue(" latency ")
	if got := diagSummaries(validateAnomaly(t, m)); !strings.Contains(got, "surrounding whitespace") {
		t.Fatalf("whitespace around detection_function_field was not rejected:\n%s", got)
	}
	m.DetectionFunctionField = types.StringValue("latency")
	if diags := validateAnomaly(t, m); diags.HasError() {
		t.Fatalf("a trimmed field was rejected:\n%s", diagSummaries(diags))
	}
}

func TestAnomalyUpdateBodyScheduleInterval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	state := anomalyModel()
	plan := anomalyModel()
	plan.Description = types.StringValue("only the description changed")
	var diags diag.Diagnostics
	body := anomalyUpdateBody(ctx, &plan, &state, &diags)
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "schedule_interval") {
		t.Errorf("an unchanged schedule_interval was sent: %s", encoded)
	}

	plan.ScheduleInterval = types.StringValue("30m")
	body = anomalyUpdateBody(ctx, &plan, &state, &diags)
	encoded, _ = json.Marshal(body)
	if !strings.Contains(string(encoded), `"schedule_interval":"30m"`) {
		t.Errorf("a changed schedule_interval was not sent: %s", encoded)
	}
}

// TestAnomalyOwnerPlanForNullOwnerImport covers an alert imported without an
// owner: an update must plan the prior null, not unknown, so the plan stays
// quiet and the null read back after apply matches it.
func TestAnomalyOwnerPlanForNullOwnerImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	s := anomalySchema(t)
	prior := anomalyModel()
	prior.AnomalyID = types.StringValue("abc")
	state := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if diags := state.Set(ctx, &prior); diags.HasError() {
		t.Fatalf("state: %+v", diags)
	}

	run := func(st tfsdk.State, config, planValue types.String) types.String {
		req := planmodifier.StringRequest{
			Path:        path.Root("owner"),
			State:       st,
			StateValue:  types.StringNull(),
			ConfigValue: config,
			PlanValue:   planValue,
		}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		keepStateWhenUnset{}.PlanModifyString(ctx, req, resp)
		return resp.PlanValue
	}

	if got := run(state, types.StringNull(), types.StringUnknown()); !got.IsNull() {
		t.Errorf("update with a null prior owner planned %v, want null", got)
	}
	if got := run(state, types.StringValue("a@example.com"), types.StringValue("a@example.com")); got.ValueString() != "a@example.com" {
		t.Errorf("a configured owner was replaced: %v", got)
	}
	create := tfsdk.State{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	if got := run(create, types.StringNull(), types.StringUnknown()); !got.IsUnknown() {
		t.Errorf("create planned %v, want unknown so the server can pick the owner", got)
	}
}

func TestKeepPlannedTrainingFields(t *testing.T) {
	t.Parallel()

	planned := anomalyModel()
	planned.BandK = types.Float64Value(4)
	planned.Status = types.StringValue("training")
	planned.TrainingDataEndUs = types.Int64Unknown()
	got := anomalyModel()
	got.BandK = types.Float64Value(5)
	got.Status = types.StringValue("ready")
	got.TrainingDataEndUs = types.Int64Value(200)
	keepPlannedTrainingFields(&planned, &got)
	if got.BandK.ValueFloat64() != 4 || got.Status.ValueString() != "training" {
		t.Errorf("planned training state was not kept: band_k %v, status %v", got.BandK, got.Status)
	}
	if got.TrainingDataEndUs.ValueInt64() != 200 {
		t.Errorf("an unknown planned value was not filled from the server: %v", got.TrainingDataEndUs)
	}
}

// int64AttrDiags runs the schema validators of one integer attribute.
func int64AttrDiags(t *testing.T, name string, value int64) diag.Diagnostics {
	t.Helper()
	attribute, ok := anomalySchema(t).Attributes[name].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("%s is not an integer attribute", name)
	}
	req := validator.Int64Request{Path: path.Root(name), ConfigValue: types.Int64Value(value)}
	var diags diag.Diagnostics
	for _, v := range attribute.Validators {
		resp := &validator.Int64Response{}
		v.ValidateInt64(context.Background(), req, resp)
		diags.Append(resp.Diagnostics...)
	}
	return diags
}

// TestAnomalyInt32Bounds checks the fields the server stores as 32-bit
// integers reject values that would not fit, at plan time.
func TestAnomalyInt32Bounds(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"training_window_days", "alert_window_buckets", "retrain_interval_days"} {
		if diags := int64AttrDiags(t, name, 1<<31); !diags.HasError() {
			t.Errorf("%s = 2^31 was accepted", name)
		}
		if diags := int64AttrDiags(t, name, 7); diags.HasError() {
			t.Errorf("%s = 7 was rejected: %s", name, diagSummaries(diags))
		}
	}
}

func TestHistogramOriginValidator(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"30s", true}, {"1m", true}, {"5m", true}, {"90m", true}, {"1h", true}, {"6h", true}, {"1d", true},
		{"7m", false}, {"11m", false}, {"5h", false}, {"7h", false}, {"10h", false}, {"2d", false},
		{"bogus", true}, // left to the grammar validator
	} {
		req := validator.StringRequest{Path: path.Root("histogram_interval"), ConfigValue: types.StringValue(tc.value)}
		resp := &validator.StringResponse{}
		histogramOriginValidator{}.ValidateString(context.Background(), req, resp)
		if resp.Diagnostics.HasError() == tc.ok {
			t.Errorf("histogram_interval %q: ok = %v, want %v", tc.value, !resp.Diagnostics.HasError(), tc.ok)
		}
	}
}

// TestAnomalyLegacyEmptyObjectFilters reads a row written before the server
// normalized filters, which can hold `{}` for "no filters".
func TestAnomalyLegacyEmptyObjectFilters(t *testing.T) {
	t.Parallel()
	elemType := types.ObjectType{AttrTypes: anomalyFilterAttrTypes}
	for _, raw := range []string{`{}`, ` { } `, `null`, `[]`} {
		var diags diag.Diagnostics
		got := anomalyFiltersToList(json.RawMessage(raw), types.ListNull(elemType), &diags)
		if diags.HasError() || !got.IsNull() {
			t.Errorf("filters %s read as %v, %v; want null", raw, got, diagSummaries(diags))
		}
	}
	var diags diag.Diagnostics
	anomalyFiltersToList(json.RawMessage(`{"field":"x"}`), types.ListNull(elemType), &diags)
	if !diags.HasError() {
		t.Error("a non-empty filters object was accepted")
	}
}

// TestAnomalyBandPreCheckWithoutListPermission covers an RBAC user who may
// create alerts but not list them: the pre-check steps aside, and the
// read-back after create decides.
func TestAnomalyBandPreCheckWithoutListPermission(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Unauthorized Access"))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")
	known, _, err := c.AnomalyBandSettingsSupported(context.Background(), "default")
	if err != nil || known {
		t.Errorf("known %v, err %v; want an undecided pre-check and no error", known, err)
	}
}

// TestAnomalyUnauthorizedOrganization reflects the shared Enterprise server,
// which answers an organization the user cannot see with 401: that is not a
// missing alert, since it can equally be bad credentials, and the diagnostic
// says both.
func TestAnomalyUnauthorizedOrganization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized Access"))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")

	got, err := c.GetAnomalyConfig(ctx, "no_such_org", "abc")
	if err == nil || got != nil {
		t.Fatalf("401 read = %+v, %v; want an error", got, err)
	}
	summary, detail := anomalyErrorDiag("importing", err)
	if !strings.Contains(summary, "unauthorized") || !strings.Contains(detail, "org_id") ||
		!strings.Contains(detail, "credentials") {
		t.Errorf("diagnostic = %q / %q", summary, detail)
	}
}
