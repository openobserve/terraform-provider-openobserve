package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func stringSet(v ...string) types.Set {
	values := make([]attr.Value, 0, len(v))
	for _, s := range v {
		values = append(values, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, values)
}

// A configured setting is sent as written, overriding what the server holds,
// and `""` clears a string setting: the field is left out of the PUT, and
// state keeps `""` so the next plan is clean.
func TestAlertUpdateSendsConfiguredIncidentFields(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	fake.put(testAlertPath, storedRecoveringAlert())
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	configured := func(description string) AlertResourceModel {
		m := alertModel(description, types.BoolValue(true))
		m.RecoveryDestinations = stringSet("d3")
		m.KeepFiringFor = types.Int64Value(300)
		m.SuppressRepeats = types.BoolValue(false)
		m.RunbookURL = types.StringValue("https://runbooks.example/new")
		m.OncallTeam = types.StringValue("")
		return m
	}
	prior := alertModel("before", types.BoolValue(true))
	prior.RecoveryDestinations = stringSet("d2")
	prior.KeepFiringFor = types.Int64Value(120)
	prior.SuppressRepeats = types.BoolValue(true)
	prior.RunbookURL = types.StringValue("https://runbooks.example/high-errors")
	prior.OncallTeam = types.StringValue("team-1")

	req, resp := updateRequest(t, r, configured("after"), prior, configured("after"))
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}

	put := fake.lastPut
	if put["runbook_url"] != "https://runbooks.example/new" {
		t.Errorf("PUT runbook_url: got %v", put["runbook_url"])
	}
	if _, ok := put["oncall_team"]; ok {
		t.Errorf("PUT oncall_team: got %v, want the field left out to clear it", put["oncall_team"])
	}
	if put["incident_title_template"] != "{alert_name} on {host}" {
		t.Errorf("PUT incident_title_template: got %v, want the server's, which has no attribute", put["incident_title_template"])
	}
	if put["keep_firing_for"] != float64(300) {
		t.Errorf("PUT keep_firing_for: got %v, want 300", put["keep_firing_for"])
	}
	if v, ok := put["suppress_repeats"]; ok && v != false {
		t.Errorf("PUT suppress_repeats: got %v, want false", v)
	}
	if got, _ := put["recovery_destinations"].([]any); len(got) != 1 || got[0] != "d3" {
		t.Errorf("PUT recovery_destinations: got %v, want [d3]", put["recovery_destinations"])
	}

	var state AlertResourceModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("decode state: %+v", diags)
	}
	if state.OncallTeam.IsNull() || state.OncallTeam.ValueString() != "" {
		t.Errorf("state oncall_team: got %v, want \"\" as configured", state.OncallTeam)
	}
	if state.KeepFiringFor.ValueInt64() != 300 || state.SuppressRepeats.ValueBool() {
		t.Errorf("state keep_firing_for/suppress_repeats: got %v/%v", state.KeepFiringFor, state.SuppressRepeats)
	}
	if !state.RecoveryDestinations.Equal(stringSet("d3")) {
		t.Errorf("state recovery_destinations: got %v", state.RecoveryDestinations)
	}
}

// Unset in the configuration, the settings read back from the server into
// state, so an import or a first apply after upgrading shows them.
func TestAlertReadReportsIncidentFields(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	fake.put(testAlertPath, storedRecoveringAlert())
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	model := alertModel("before", types.BoolValue(true))
	r.applyAlertToModel(context.Background(), mustGetAlert(t, r), &model, &diag.Diagnostics{})
	if model.RunbookURL.ValueString() != "https://runbooks.example/high-errors" ||
		model.OncallTeam.ValueString() != "team-1" {
		t.Errorf("strings: got %v %v", model.RunbookURL, model.OncallTeam)
	}
	if model.KeepFiringFor.ValueInt64() != 120 || !model.SuppressRepeats.ValueBool() {
		t.Errorf("keep_firing_for/suppress_repeats: got %v/%v", model.KeepFiringFor, model.SuppressRepeats)
	}
	if !model.RecoveryDestinations.Equal(stringSet("d2")) {
		t.Errorf("recovery_destinations: got %v", model.RecoveryDestinations)
	}

	plain := alertModel("before", types.BoolValue(false))
	stored := storedAlert(false)
	fake.put(testAlertPath, stored)
	r.applyAlertToModel(context.Background(), mustGetAlert(t, r), &plain, &diag.Diagnostics{})
	if !plain.RunbookURL.IsNull() || !plain.OncallTeam.IsNull() {
		t.Errorf("absent strings must read as null: %v %v", plain.RunbookURL, plain.OncallTeam)
	}
	if plain.RecoveryDestinations.IsNull() || len(plain.RecoveryDestinations.Elements()) != 0 {
		t.Errorf("absent recovery_destinations must read as an empty set: %v", plain.RecoveryDestinations)
	}
}

// A configured promql_rule_mode is sent as written, false included, rather
// than the server's.
func TestAlertUpdateSendsConfiguredRuleMode(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	stored := storedAlert(false)
	stored["stream_type"] = "metrics"
	stored["query_condition"] = map[string]any{"type": "promql", "promql": "up == 0", "promql_rule_mode": true}
	fake.put(testAlertPath, stored)
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	model := func(description string, ruleMode types.Bool) AlertResourceModel {
		m := alertModel(description, types.BoolValue(false))
		m.StreamType = types.StringValue("metrics")
		m.QueryCondition.QueryType = types.StringValue("promql")
		m.QueryCondition.SQL = types.StringNull()
		m.QueryCondition.PromQL = types.StringValue("up == 0")
		m.QueryCondition.PromQLRuleMode = ruleMode
		return m
	}
	req, resp := updateRequest(t, r, model("after", types.BoolValue(false)), model("before", types.BoolValue(true)),
		model("after", types.BoolValue(false)))
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}
	qc, _ := fake.lastPut["query_condition"].(map[string]any)
	if v, ok := qc["promql_rule_mode"]; ok && v != false {
		t.Errorf("PUT promql_rule_mode: got %v, want false", v)
	}
}

func TestRunbookURLProblem(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{
		"https://runbooks.example/checkout",
		"http://wiki.internal:8080/a?b=c#d",
		"HTTPS://example.com",
	} {
		if p := runbookURLProblem(ok); p != "" {
			t.Errorf("%q: unexpected problem %q", ok, p)
		}
	}
	for raw, want := range map[string]string{
		"wiki/runbooks/checkout":                 "must start with",
		"ftp://example.com":                      "must start with",
		"javascript://x":                         "must start with",
		"https:///runbooks":                      "no host",
		"https://?q":                             "no host",
		"https://example.com/a b":                "whitespace",
		" https://example.com":                   "whitespace",
		"   ":                                    "blank",
		"https://x/" + strings.Repeat("a", 2048): "longer than 2048",
	} {
		if p := runbookURLProblem(raw); !strings.Contains(p, want) {
			t.Errorf("%q: got problem %q, want one mentioning %q", raw, p, want)
		}
	}
}

func TestClearedStringToModel(t *testing.T) {
	t.Parallel()
	value := "x"
	empty := ""
	cases := []struct {
		name   string
		prior  types.String
		server *string
		want   types.String
	}{
		{"set", types.StringNull(), &value, types.StringValue("x")},
		{"absent, never configured", types.StringNull(), nil, types.StringNull()},
		{"absent, configured empty", types.StringValue(""), nil, types.StringValue("")},
		{"empty, configured empty", types.StringValue(""), &empty, types.StringValue("")},
		{"absent, was set", types.StringValue("old"), nil, types.StringNull()},
		{"absent, unknown", types.StringUnknown(), nil, types.StringNull()},
	}
	for _, tc := range cases {
		if got := clearedStringToModel(tc.prior, tc.server); !got.Equal(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if clearableString(types.StringValue("")) != nil || clearableString(types.StringNull()) != nil {
		t.Error("clearableString must send nothing for \"\" and null")
	}
}

func TestAlertValidateConfigIncidentFields(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		edit    func(m *AlertResourceModel)
		wantErr string
	}{
		"valid recovery setup": {func(m *AlertResourceModel) {
			m.NotifyOnRecovery = types.BoolValue(true)
			m.RecoveryDestinations = stringSet("d2")
			m.KeepFiringFor = types.Int64Value(86400)
			m.SuppressRepeats = types.BoolValue(true)
		}, ""},
		"keep_firing_for too long": {func(m *AlertResourceModel) {
			m.KeepFiringFor = types.Int64Value(86401)
		}, "out of range"},
		"keep_firing_for negative": {func(m *AlertResourceModel) {
			m.KeepFiringFor = types.Int64Value(-1)
		}, "out of range"},
		"keep_firing_for on real-time": {func(m *AlertResourceModel) {
			m.IsRealTime = types.BoolValue(true)
			m.KeepFiringFor = types.Int64Value(60)
		}, "real-time"},
		"keep_firing_for zero on real-time": {func(m *AlertResourceModel) {
			m.IsRealTime = types.BoolValue(true)
			m.KeepFiringFor = types.Int64Value(0)
		}, ""},
		"suppress_repeats on real-time": {func(m *AlertResourceModel) {
			m.IsRealTime = types.BoolValue(true)
			m.SuppressRepeats = types.BoolValue(true)
		}, "real-time"},
		"recovery_destinations with recovery off": {func(m *AlertResourceModel) {
			m.NotifyOnRecovery = types.BoolValue(false)
			m.RecoveryDestinations = stringSet("d2")
		}, "needs notify_on_recovery"},
		"empty recovery_destinations with recovery off": {func(m *AlertResourceModel) {
			m.NotifyOnRecovery = types.BoolValue(false)
			m.RecoveryDestinations = stringSet()
		}, ""},
		"recovery_destinations on real-time": {func(m *AlertResourceModel) {
			m.IsRealTime = types.BoolValue(true)
			m.RecoveryDestinations = stringSet("d2")
		}, "real-time"},
		"bad runbook_url": {func(m *AlertResourceModel) {
			m.RunbookURL = types.StringValue("wiki/runbooks/checkout")
		}, "Invalid runbook_url"},
		"empty runbook_url clears": {func(m *AlertResourceModel) {
			m.RunbookURL = types.StringValue("")
		}, ""},
		"rule mode on sql": {func(m *AlertResourceModel) {
			m.QueryCondition.PromQLRuleMode = types.BoolValue(true)
		}, "needs a PromQL alert"},
		"rule mode on logs": {func(m *AlertResourceModel) {
			promql(m)
			m.StreamType = types.StringValue("logs")
		}, "metrics stream"},
		"rule mode with pending period": {func(m *AlertResourceModel) {
			promql(m)
			m.PendingPeriodSec = types.Int64Value(60)
		}, "multi-alert"},
		"rule mode with pending period and multi-alert": {func(m *AlertResourceModel) {
			promql(m)
			m.PendingPeriodSec = types.Int64Value(60)
			m.QueryCondition.PromQLMultiAlert = types.BoolValue(true)
		}, ""},
		"rule mode with = 0": {func(m *AlertResourceModel) {
			promql(m)
			m.TriggerCondition.Operator = types.StringValue("=")
			m.TriggerCondition.Threshold = types.Int64Value(0)
		}, "any returned series"},
		"rule mode": {func(m *AlertResourceModel) { promql(m) }, ""},
		"rule mode with > 0": {func(m *AlertResourceModel) {
			promql(m)
			m.TriggerCondition.Operator = types.StringValue(">")
			m.TriggerCondition.Threshold = types.Int64Value(0)
		}, ""},
		"rule mode with >= 3": {func(m *AlertResourceModel) {
			promql(m)
			m.TriggerCondition.Threshold = types.Int64Value(3)
		}, "accepts only"},
		"rule mode with promql_condition": {func(m *AlertResourceModel) {
			promql(m)
			m.QueryCondition.PromQLCondition = &AlertConditionModel{
				Column: types.StringValue("value"), Operator: types.StringValue(">="), Value: types.StringValue("0"),
			}
		}, "refuses `promql_condition`"},
		"rule mode with a warning value": {func(m *AlertResourceModel) {
			promql(m)
			m.QueryCondition.PromQLWarningValue = types.Float64Value(1)
		}, "refuses `promql_warning_value`"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := &AlertResource{}
			m := alertModel("d", types.BoolNull())
			tc.edit(&m)
			cfg := tfValue(t, r, m)
			resp := &resource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(cfg)}, resp)
			// Attribute validators run in the framework, not in ValidateConfig.
			vr := &validator.StringResponse{}
			runbookURLValidator{}.ValidateString(context.Background(),
				validator.StringRequest{Path: path.Root("runbook_url"), ConfigValue: m.RunbookURL}, vr)
			resp.Diagnostics.Append(vr.Diagnostics...)
			got := ""
			for _, d := range resp.Diagnostics.Errors() {
				got += d.Summary() + ": " + d.Detail() + "\n"
			}
			switch {
			case tc.wantErr == "" && got != "":
				t.Errorf("unexpected errors:\n%s", got)
			case tc.wantErr != "" && !strings.Contains(got, tc.wantErr):
				t.Errorf("want an error mentioning %q, got:\n%s", tc.wantErr, got)
			}
		})
	}
}

func promql(m *AlertResourceModel) {
	m.StreamType = types.StringValue("metrics")
	m.QueryCondition.QueryType = types.StringValue("promql")
	m.QueryCondition.SQL = types.StringNull()
	m.QueryCondition.PromQL = types.StringValue("rate(errors[5m]) > 0.1")
	m.QueryCondition.PromQLRuleMode = types.BoolValue(true)
}

func mustGetAlert(t *testing.T, r *AlertResource) *AlertAPI {
	t.Helper()
	alert, err := r.client.GetAlert(context.Background(), "default", "a1")
	if err != nil || alert == nil {
		t.Fatalf("get alert: %v, %v", alert, err)
	}
	return alert
}
