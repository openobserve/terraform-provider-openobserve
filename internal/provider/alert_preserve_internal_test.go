package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// replacingServer stands in for an OpenObserve endpoint whose PUT replaces the
// stored object with the request body, as the alert and destination update
// endpoints do. A field the body leaves out is gone afterwards.
type replacingServer struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string]map[string]any // by request path
	lastPut map[string]any
	puts    int
}

func newReplacingServer(t *testing.T) (*replacingServer, *httptest.Server) {
	t.Helper()
	s := &replacingServer{t: t, objects: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *replacingServer) put(path string, obj map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[path] = obj
}

func (s *replacingServer) get(path string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[path]
}

func (s *replacingServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		if r.URL.Path == "/api/v2/default/alerts" {
			_, _ = w.Write([]byte(`{"list":[]}`))
			return
		}
		obj, ok := s.objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(obj)
	case http.MethodPut, http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			s.t.Errorf("%s %s: body is not JSON: %v", r.Method, r.URL.Path, err)
		}
		path := r.URL.Path
		if r.Method == http.MethodPost {
			path += "/" + body["name"].(string)
		} else {
			s.lastPut = body
			s.puts++
		}
		s.objects[path] = body
		_, _ = w.Write([]byte(`{"code":200}`))
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

// tfValue encodes a model under a resource's schema, the way Terraform hands
// the plan, the prior state and the configuration to the provider.
func tfValue(t *testing.T, res resource.Resource, model any) tfsdk.State {
	t.Helper()
	ctx := context.Background()
	schemaResp := &resource.SchemaResponse{}
	res.Schema(ctx, resource.SchemaRequest{}, schemaResp)
	st := tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	if diags := st.Set(ctx, model); diags.HasError() {
		t.Fatalf("encode model: %+v", diags)
	}
	return st
}

// updateRequest builds the request Terraform sends for an update.
func updateRequest(t *testing.T, res resource.Resource, plan, state, config any) (resource.UpdateRequest, *resource.UpdateResponse) {
	t.Helper()
	p, s, c := tfValue(t, res, plan), tfValue(t, res, state), tfValue(t, res, config)
	req := resource.UpdateRequest{
		Plan:   tfsdk.Plan(p),
		State:  s,
		Config: tfsdk.Config(c),
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: p.Schema, Raw: p.Raw.Copy()}}
	return req, resp
}

// ---------------------------------------------------------------------------
// openobserve_alert: notify_on_recovery
// ---------------------------------------------------------------------------

const testAlertPath = "/api/v2/default/alerts/a1"

func storedAlert(notifyOnRecovery bool) map[string]any {
	return map[string]any{
		"id":                 "a1",
		"name":               "tf_i9_alert",
		"stream_type":        "logs",
		"stream_name":        "app",
		"is_real_time":       false,
		"query_condition":    map[string]any{"type": "sql", "sql": "SELECT count(*) AS total FROM app"},
		"trigger_condition":  map[string]any{"period": 15, "operator": ">=", "threshold": 1, "frequency": 5, "silence": 60, "frequency_type": "minutes"},
		"destinations":       []string{"d1"},
		"description":        "before",
		"enabled":            true,
		"row_template":       "",
		"row_template_type":  "String",
		"notify_on_recovery": notifyOnRecovery,
	}
}

// storedRecoveringAlert is an alert whose recovery was set up in the UI,
// including the settings that have no attribute.
func storedRecoveringAlert() map[string]any {
	a := storedAlert(true)
	a["recovery_destinations"] = []string{"d2"}
	a["keep_firing_for"] = 120
	a["suppress_repeats"] = true
	a["runbook_url"] = "https://runbooks.example/high-errors"
	a["oncall_team"] = "team-1"
	a["incident_title_template"] = "{alert_name} on {host}"
	return a
}

// alertModel is an alert as the provider would hold it, every collection typed.
func alertModel(description string, notifyOnRecovery types.Bool) AlertResourceModel {
	return AlertResourceModel{
		ID:                   types.StringValue("default/a1"),
		AlertID:              types.StringValue("a1"),
		OrgID:                types.StringValue("default"),
		FolderID:             types.StringValue("default"),
		Name:                 types.StringValue("tf_i9_alert"),
		StreamType:           types.StringValue("logs"),
		StreamName:           types.StringValue("app"),
		IsRealTime:           types.BoolValue(false),
		Enabled:              types.BoolValue(true),
		Description:          types.StringValue(description),
		Destinations:         types.SetValueMust(types.StringType, []attr.Value{types.StringValue("d1")}),
		ContextAttributes:    types.MapNull(types.StringType),
		RowTemplate:          types.StringValue(""),
		RowTemplateType:      types.StringValue("String"),
		Tags:                 types.SetNull(types.StringType),
		CreatesIncident:      types.BoolValue(false),
		Workflows:            types.SetNull(types.StringType),
		PendingPeriodSec:     types.Int64Value(0),
		NotifyOnRecovery:     notifyOnRecovery,
		RecoveryDestinations: types.SetNull(types.StringType),
		QueryCondition: &AlertQueryConditionModel{
			QueryType:      types.StringValue("sql"),
			SQL:            types.StringValue("SELECT count(*) AS total FROM app"),
			MultiTimeRange: types.ListNull(types.ObjectType{AttrTypes: map[string]attr.Type{"offset": types.StringType}}),
		},
		TriggerCondition: &AlertTriggerConditionModel{
			Period:        types.Int64Value(15),
			Operator:      types.StringValue(">="),
			Threshold:     types.Int64Value(1),
			Frequency:     types.Int64Value(5),
			FrequencyType: types.StringValue("minutes"),
			Silence:       types.Int64Value(60),
			AlignTime:     types.BoolValue(true),
		},
	}
}

// runAlertUpdate applies an update that changes the description, against a
// server holding storedRecoveringAlert. realTime is applied to plan, state and
// configuration alike.
func runAlertUpdate(t *testing.T, plannedRecovery, priorRecovery, configuredRecovery types.Bool, realTime bool) (*replacingServer, AlertResourceModel, *resource.UpdateResponse) {
	t.Helper()
	fake, srv := newReplacingServer(t)
	fake.put(testAlertPath, storedRecoveringAlert())
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	model := func(description string, recovery types.Bool) AlertResourceModel {
		m := alertModel(description, recovery)
		m.IsRealTime = types.BoolValue(realTime)
		return m
	}
	req, resp := updateRequest(t, r,
		model("after", plannedRecovery),
		model("before", priorRecovery),
		model("after", configuredRecovery),
	)
	r.Update(context.Background(), req, resp)
	var got AlertResourceModel
	if !resp.Diagnostics.HasError() {
		if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
			t.Fatalf("decode state: %+v", diags)
		}
	}
	return fake, got, resp
}

// Settings with no attribute survive an update, whatever the configuration
// says about notify_on_recovery and on a real-time alert too, because the
// whole-alert replace would otherwise clear them.
func TestAlertUpdateKeepsUnmanagedSettings(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		configured types.Bool
		realTime   bool
	}{
		"recovery unset":    {types.BoolNull(), false},
		"recovery off":      {types.BoolValue(false), false},
		"real-time, off":    {types.BoolValue(false), true},
		"recovery explicit": {types.BoolValue(true), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			planned := tc.configured
			if planned.IsNull() {
				planned = types.BoolValue(true)
			}
			fake, _, resp := runAlertUpdate(t, planned, types.BoolValue(true), tc.configured, tc.realTime)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %+v", resp.Diagnostics)
			}
			want := map[string]string{
				"runbook_url":             "https://runbooks.example/high-errors",
				"oncall_team":             "team-1",
				"incident_title_template": "{alert_name} on {host}",
			}
			for k, v := range want {
				if fake.lastPut[k] != v {
					t.Errorf("PUT %s: got %v, want %q", k, fake.lastPut[k], v)
				}
				if fake.get(testAlertPath)[k] != v {
					t.Errorf("server lost %s across the update", k)
				}
			}
		})
	}
}

// Optional+Computed attributes left unset keep the server's value: the
// destinations list (one added in the UI must not be removed), the trigger's
// operator and threshold, and the aggregation's group_by. The update sends the
// server's value; state holds the planned one, which differs only when the
// server changed after the plan.
func TestAlertUpdateKeepsUnsetComputedAttributes(t *testing.T) {
	t.Parallel()
	setOf := func(v ...string) types.Set {
		values := make([]attr.Value, 0, len(v))
		for _, s := range v {
			values = append(values, types.StringValue(s))
		}
		return types.SetValueMust(types.StringType, values)
	}
	listOf := func(v ...string) types.List {
		values := make([]attr.Value, 0, len(v))
		for _, s := range v {
			values = append(values, types.StringValue(s))
		}
		return types.ListValueMust(types.StringType, values)
	}
	cases := map[string]struct {
		planned  types.Set
		operator string
		groupBy  types.List
	}{
		// The refresh before the plan read the UI's changes into state.
		"refreshed state": {setOf("d1", "ui_added"), ">", listOf("host")},
		// The UI changed them after the plan was made.
		"changed after the plan": {setOf("d1"), ">=", listOf("service")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, srv := newReplacingServer(t)
			stored := storedAlert(false)
			stored["destinations"] = []string{"d1", "ui_added"}
			stored["query_condition"] = map[string]any{
				"type": "custom",
				"aggregation": map[string]any{
					"group_by": []string{"host"},
					"function": "count",
					"having":   map[string]any{"column": "total", "operator": ">", "value": 10},
				},
			}
			trigger, _ := stored["trigger_condition"].(map[string]any)
			trigger["operator"] = ">"
			trigger["threshold"] = 5
			fake.put(testAlertPath, stored)
			r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

			model := func(description string, dests types.Set, operator types.String, threshold types.Int64, groupBy types.List) AlertResourceModel {
				m := alertModel(description, types.BoolValue(false))
				m.Destinations = dests
				m.QueryCondition.QueryType = types.StringValue("custom")
				m.QueryCondition.SQL = types.StringNull()
				m.QueryCondition.Aggregation = &AlertAggregationModel{
					GroupBy:  groupBy,
					Function: types.StringValue("count"),
					Having: &AlertConditionModel{
						Column:   types.StringValue("total"),
						Operator: types.StringValue(">"),
						Value:    types.StringValue("10"),
					},
				}
				m.TriggerCondition.Operator = operator
				m.TriggerCondition.Threshold = threshold
				return m
			}
			plan := model("after", tc.planned, types.StringValue(tc.operator), types.Int64Value(5), tc.groupBy)
			req, resp := updateRequest(t, r,
				plan,
				model("before", tc.planned, types.StringValue(tc.operator), types.Int64Value(5), tc.groupBy),
				model("after", types.SetNull(types.StringType), types.StringNull(), types.Int64Null(), types.ListNull(types.StringType)),
			)
			r.Update(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %+v", resp.Diagnostics)
			}

			dests, _ := fake.lastPut["destinations"].([]any)
			if len(dests) != 2 {
				t.Errorf("PUT destinations: got %v, want the server's [d1 ui_added]", fake.lastPut["destinations"])
			}
			tc2, _ := fake.lastPut["trigger_condition"].(map[string]any)
			if tc2["operator"] != ">" || tc2["threshold"] != float64(5) {
				t.Errorf("PUT trigger: got operator %v threshold %v, want the server's > 5", tc2["operator"], tc2["threshold"])
			}
			qc, _ := fake.lastPut["query_condition"].(map[string]any)
			agg, _ := qc["aggregation"].(map[string]any)
			if gb, _ := agg["group_by"].([]any); len(gb) != 1 || gb[0] != "host" {
				t.Errorf("PUT group_by: got %v, want the server's [host]", agg["group_by"])
			}

			var got AlertResourceModel
			resp.State.Get(context.Background(), &got)
			if !got.Destinations.Equal(tc.planned) {
				t.Errorf("state destinations: got %v, want the planned %v", got.Destinations, tc.planned)
			}
			if got.TriggerCondition.Operator.ValueString() != tc.operator {
				t.Errorf("state operator: got %v, want the planned %q", got.TriggerCondition.Operator, tc.operator)
			}
			if !got.QueryCondition.Aggregation.GroupBy.Equal(tc.groupBy) {
				t.Errorf("state group_by: got %v, want the planned %v", got.QueryCondition.Aggregation.GroupBy, tc.groupBy)
			}
		})
	}
}

// A change of alert family moves the trigger to the new family's defaults
// rather than carrying the old family's operator and threshold over, which an
// SLO alert would refuse.
func TestKeepServerComputedFamilyChange(t *testing.T) {
	t.Parallel()
	config := alertModel("x", types.BoolNull())
	config.TriggerCondition.Operator = types.StringNull()
	config.TriggerCondition.Threshold = types.Int64Null()
	current := &AlertAPI{
		QueryCondition:   AlertQueryConditionAPI{QueryType: "sql"},
		TriggerCondition: AlertTriggerConditionAPI{Operator: ">", Threshold: 5},
	}

	body := &AlertAPI{QueryCondition: AlertQueryConditionAPI{QueryType: "slo"}}
	keepServerComputed(&config, current, body)
	if body.TriggerCondition.Operator != "=" || body.TriggerCondition.Threshold != 0 {
		t.Errorf("sql to slo: got %q %d, want the SLO defaults = 0", body.TriggerCondition.Operator, body.TriggerCondition.Threshold)
	}

	body = &AlertAPI{QueryCondition: AlertQueryConditionAPI{QueryType: "custom"}}
	keepServerComputed(&config, current, body)
	if body.TriggerCondition.Operator != ">" || body.TriggerCondition.Threshold != 5 {
		t.Errorf("sql to custom: got %q %d, want the server's > 5", body.TriggerCondition.Operator, body.TriggerCondition.Threshold)
	}
}

// promql_rule_mode is kept while the alert stays a PromQL alert.
func TestAlertUpdateKeepsPromQLRuleMode(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	stored := storedAlert(false)
	stored["stream_type"] = "metrics"
	stored["query_condition"] = map[string]any{"type": "promql", "promql": "up == 0", "promql_rule_mode": true}
	fake.put(testAlertPath, stored)
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	model := func(description string) AlertResourceModel {
		m := alertModel(description, types.BoolValue(false))
		m.StreamType = types.StringValue("metrics")
		m.QueryCondition.QueryType = types.StringValue("promql")
		m.QueryCondition.SQL = types.StringNull()
		m.QueryCondition.PromQL = types.StringValue("up == 0")
		return m
	}
	req, resp := updateRequest(t, r, model("after"), model("before"), model("after"))
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}
	qc, _ := fake.lastPut["query_condition"].(map[string]any)
	if qc["promql_rule_mode"] != true {
		t.Errorf("PUT query_condition.promql_rule_mode: got %v, want true", qc["promql_rule_mode"])
	}
}

// The update endpoint replaces the whole alert, so an update that left the
// flag out, or sent the schema default, would switch off recovery
// notifications someone turned on in the UI. Unset, the server's value is
// sent, and so are the recovery settings that have no attribute.
func TestAlertUpdateKeepsServerNotifyOnRecoveryWhenUnset(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ planned, prior types.Bool }{
		// The usual case: the refresh before the plan read true into state.
		"refreshed state": {types.BoolValue(true), types.BoolValue(true)},
		// State written by 1.4.3, which had no such attribute.
		"state from an older release": {types.BoolUnknown(), types.BoolNull()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, got, resp := runAlertUpdate(t, tc.planned, tc.prior, types.BoolNull(), false)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %+v", resp.Diagnostics)
			}

			if fake.puts != 1 {
				t.Fatalf("PUTs: got %d, want 1", fake.puts)
			}
			if v, ok := fake.lastPut["notify_on_recovery"]; !ok || v != true {
				t.Errorf("PUT notify_on_recovery: got %v (present %v), want true", v, ok)
			}
			if fake.lastPut["description"] != "after" {
				t.Errorf("PUT description: got %v, want the planned change", fake.lastPut["description"])
			}
			if fake.get(testAlertPath)["notify_on_recovery"] != true {
				t.Error("server lost notify_on_recovery across the update")
			}
			if !got.NotifyOnRecovery.ValueBool() {
				t.Errorf("state notify_on_recovery: got %v, want true", got.NotifyOnRecovery)
			}
			assertRecoverySettingsKept(t, fake.lastPut, true)
		})
	}
}

// The server's value can change between plan and apply: switched on in the UI
// after the plan, or a plan made with -refresh=false. The update must still
// send the server's value, and state must hold the planned value, because
// Terraform fails an apply whose result differs from a known planned value.
func TestAlertUpdateServerFlipBetweenPlanAndApply(t *testing.T) {
	t.Parallel()
	fake, got, resp := runAlertUpdate(t, types.BoolValue(false), types.BoolValue(false), types.BoolNull(), false)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}
	if fake.lastPut["notify_on_recovery"] != true {
		t.Errorf("PUT notify_on_recovery: got %v, want the server's true", fake.lastPut["notify_on_recovery"])
	}
	if fake.get(testAlertPath)["notify_on_recovery"] != true {
		t.Error("server lost notify_on_recovery across the update")
	}
	if !got.NotifyOnRecovery.Equal(types.BoolValue(false)) {
		t.Errorf("state notify_on_recovery: got %v, want the planned false", got.NotifyOnRecovery)
	}
	assertRecoverySettingsKept(t, fake.lastPut, true)
}

func assertRecoverySettingsKept(t *testing.T, put map[string]any, wantDestinations bool) {
	t.Helper()
	dests, _ := put["recovery_destinations"].([]any)
	if wantDestinations && (len(dests) != 1 || dests[0] != "d2") {
		t.Errorf("PUT recovery_destinations: got %v, want [d2]", put["recovery_destinations"])
	}
	if !wantDestinations && len(dests) != 0 {
		t.Errorf("PUT recovery_destinations: got %v, want none", put["recovery_destinations"])
	}
	if put["keep_firing_for"] != float64(120) {
		t.Errorf("PUT keep_firing_for: got %v, want 120", put["keep_firing_for"])
	}
	if put["suppress_repeats"] != true {
		t.Errorf("PUT suppress_repeats: got %v, want true", put["suppress_repeats"])
	}
}

// A configured value is managed: it is sent as written, whatever the server
// held, and read back into state. Turning recovery off drops the recovery
// destinations with it, because the server refuses them without the switch.
func TestAlertUpdateSendsConfiguredNotifyOnRecovery(t *testing.T) {
	t.Parallel()
	for _, want := range []bool{true, false} {
		t.Run(fmt.Sprintf("configured %v", want), func(t *testing.T) {
			t.Parallel()
			fake, got, resp := runAlertUpdate(t, types.BoolValue(want), types.BoolValue(true), types.BoolValue(want), false)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %+v", resp.Diagnostics)
			}
			if fake.lastPut["notify_on_recovery"] != want {
				t.Errorf("PUT sent %v", fake.lastPut["notify_on_recovery"])
			}
			if got.NotifyOnRecovery.ValueBool() != want {
				t.Errorf("state holds %v", got.NotifyOnRecovery)
			}
			assertRecoverySettingsKept(t, fake.lastPut, want)
		})
	}
}

// A real-time alert can carry no recovery settings. The flag is never turned
// off unasked, so an unset attribute against a server true stops the update;
// an explicit false goes through and drops the settings without an attribute.
func TestAlertUpdateToRealTime(t *testing.T) {
	t.Parallel()
	t.Run("unset", func(t *testing.T) {
		t.Parallel()
		fake, _, resp := runAlertUpdate(t, types.BoolValue(true), types.BoolValue(true), types.BoolNull(), true)
		if !resp.Diagnostics.HasError() {
			t.Fatal("want the update refused while the server holds notify_on_recovery = true")
		}
		if fake.puts != 0 {
			t.Errorf("PUTs: got %d, want none", fake.puts)
		}
	})
	t.Run("configured false", func(t *testing.T) {
		t.Parallel()
		fake, got, resp := runAlertUpdate(t, types.BoolValue(false), types.BoolValue(true), types.BoolValue(false), true)
		if resp.Diagnostics.HasError() {
			t.Fatalf("update: %+v", resp.Diagnostics)
		}
		if fake.lastPut["notify_on_recovery"] != false {
			t.Errorf("PUT notify_on_recovery: got %v, want false", fake.lastPut["notify_on_recovery"])
		}
		for _, k := range []string{"recovery_destinations", "keep_firing_for", "suppress_repeats"} {
			if v, ok := fake.lastPut[k]; ok {
				t.Errorf("PUT %s: got %v, want it left out for a real-time alert", k, v)
			}
		}
		if got.NotifyOnRecovery.ValueBool() {
			t.Error("state notify_on_recovery: got true, want false")
		}
	})
}

// Create sends a configured value and leaves an unset one to the server
// default; either way state holds what the server stored.
func TestAlertCreateNotifyOnRecovery(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		planned types.Bool
		sent    any
		want    bool
	}{
		"configured true": {types.BoolValue(true), true, true},
		"unset":           {types.BoolUnknown(), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var sent map[string]any
			var mu sync.Mutex
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost:
					_ = json.NewDecoder(r.Body).Decode(&sent)
					_, _ = w.Write([]byte(`{"code":200,"id":"a1"}`))
				case r.URL.Path == testAlertPath:
					stored := storedAlert(false)
					if v, ok := sent["notify_on_recovery"].(bool); ok {
						stored["notify_on_recovery"] = v
					}
					_ = json.NewEncoder(w).Encode(stored)
				default:
					_, _ = w.Write([]byte(`{"list":[]}`))
				}
			}))
			t.Cleanup(srv.Close)
			r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

			plan := tfValue(t, r, alertModel("before", tc.planned))
			req := resource.CreateRequest{Plan: tfsdk.Plan(plan)}
			resp := &resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema, Raw: plan.Raw.Copy()}}
			r.Create(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("create: %+v", resp.Diagnostics)
			}
			if got := sent["notify_on_recovery"]; got != tc.sent {
				t.Errorf("POST notify_on_recovery: got %v, want %v", got, tc.sent)
			}
			var got AlertResourceModel
			resp.State.Get(context.Background(), &got)
			if got.NotifyOnRecovery.IsUnknown() || got.NotifyOnRecovery.ValueBool() != tc.want {
				t.Errorf("state notify_on_recovery: got %v, want %v", got.NotifyOnRecovery, tc.want)
			}
		})
	}
}

// Read reports the server's value, so a change made elsewhere shows as drift
// and an import picks it up.
func TestAlertReadReportsNotifyOnRecovery(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	fake.put(testAlertPath, storedAlert(true))
	r := &AlertResource{client: newClient(srv.URL, "u", "p", "default")}

	state := tfValue(t, r, alertModel("before", types.BoolValue(false)))
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: state.Schema, Raw: state.Raw.Copy()}}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %+v", resp.Diagnostics)
	}
	var got AlertResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.NotifyOnRecovery.ValueBool() {
		t.Errorf("state notify_on_recovery: got %v, want the server's true", got.NotifyOnRecovery)
	}
}

// State written by 1.4.3 has no notify_on_recovery and no metadata. The
// refresh before the first plan on the new release fills both in from the
// server, so with the attributes unset the plan is clean.
func TestReadFillsAttributesMissingFromOlderState(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	fake.put(testAlertPath, storedAlert(true))
	fake.put(testDestinationPath, storedDestination(map[string]any{"purpose": "k8s"}))
	client := newClient(srv.URL, "u", "p", "default")

	alerts := &AlertResource{client: client}
	state := tfValue(t, alerts, alertModel("before", types.BoolNull()))
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: state.Schema, Raw: state.Raw.Copy()}}
	alerts.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("alert read: %+v", resp.Diagnostics)
	}
	var alert AlertResourceModel
	resp.State.Get(context.Background(), &alert)
	if alert.NotifyOnRecovery.IsNull() || !alert.NotifyOnRecovery.ValueBool() {
		t.Errorf("notify_on_recovery after read: got %v, want the server's true", alert.NotifyOnRecovery)
	}

	dests := &AlertDestinationResource{client: client}
	state = tfValue(t, dests, destinationModel("https://hooks.example/a", types.MapNull(types.StringType)))
	resp = &resource.ReadResponse{State: tfsdk.State{Schema: state.Schema, Raw: state.Raw.Copy()}}
	dests.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("destination read: %+v", resp.Diagnostics)
	}
	var dest AlertDestinationResourceModel
	resp.State.Get(context.Background(), &dest)
	if want := stringMap(map[string]string{"purpose": "k8s"}); !dest.Metadata.Equal(want) {
		t.Errorf("metadata after read: got %v, want %v", dest.Metadata, want)
	}
}

func TestAlertValidateRejectsRecoveryOnRealTime(t *testing.T) {
	t.Parallel()
	r := &AlertResource{}
	model := alertModel("x", types.BoolValue(true))
	model.IsRealTime = types.BoolValue(true)
	cfg := tfValue(t, r, model)
	resp := &resource.ValidateConfigResponse{}
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(cfg)}, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Summary(), "real-time") {
		t.Errorf("want a real-time diagnostic, got %+v", resp.Diagnostics)
	}
}

// ---------------------------------------------------------------------------
// openobserve_alert_destination: metadata
// ---------------------------------------------------------------------------

const testDestinationPath = "/api/default/alerts/destinations/tf_i9_dest"

func destinationModel(url string, metadata types.Map) AlertDestinationResourceModel {
	return AlertDestinationResourceModel{
		ID:              types.StringValue("default/tf_i9_dest"),
		OrgID:           types.StringValue("default"),
		Name:            types.StringValue("tf_i9_dest"),
		DestinationType: types.StringValue("http"),
		Template:        types.StringValue("t1"),
		URL:             types.StringValue(url),
		Method:          types.StringValue("post"),
		SkipTLSVerify:   types.BoolValue(false),
		Headers:         types.MapNull(types.StringType),
		Emails:          types.SetNull(types.StringType),
		Metadata:        metadata,
	}
}

func stringMap(kv map[string]string) types.Map {
	values := map[string]attr.Value{}
	for k, v := range kv {
		values[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, values)
}

func storedDestination(metadata map[string]any) map[string]any {
	return map[string]any{
		"name":            "tf_i9_dest",
		"type":            "http",
		"url":             "https://hooks.example/a",
		"method":          "post",
		"skip_tls_verify": false,
		"template":        "t1",
		"emails":          []string{},
		"metadata":        metadata,
	}
}

func runDestinationUpdate(t *testing.T, planned, prior, configured types.Map) (*replacingServer, AlertDestinationResourceModel) {
	t.Helper()
	fake, srv := newReplacingServer(t)
	fake.put(testDestinationPath, storedDestination(map[string]any{"purpose": "k8s", "render_format": "slack"}))
	r := &AlertDestinationResource{client: newClient(srv.URL, "u", "p", "default")}

	req, resp := updateRequest(t, r,
		destinationModel("https://hooks.example/b", planned),
		destinationModel("https://hooks.example/a", prior),
		destinationModel("https://hooks.example/b", configured),
	)
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}
	var got AlertDestinationResourceModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("decode state: %+v", diags)
	}
	return fake, got
}

// Metadata written by the UI or the API survives an update whose
// configuration does not mention it.
func TestAlertDestinationUpdateKeepsServerMetadataWhenUnset(t *testing.T) {
	t.Parallel()
	server := map[string]string{"purpose": "k8s", "render_format": "slack"}
	cases := map[string]struct{ planned, prior types.Map }{
		"refreshed state":             {stringMap(server), stringMap(server)},
		"state from an older release": {types.MapUnknown(types.StringType), types.MapNull(types.StringType)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, got := runDestinationUpdate(t, tc.planned, tc.prior, types.MapNull(types.StringType))

			sent, _ := fake.lastPut["metadata"].(map[string]any)
			if sent["purpose"] != "k8s" || sent["render_format"] != "slack" || len(sent) != 2 {
				t.Errorf("PUT metadata: got %v, want the server's map", fake.lastPut["metadata"])
			}
			if fake.lastPut["url"] != "https://hooks.example/b" {
				t.Errorf("PUT url: got %v, want the planned change", fake.lastPut["url"])
			}
			if !got.Metadata.Equal(stringMap(server)) {
				t.Errorf("state metadata: got %v, want %v", got.Metadata, server)
			}
		})
	}
}

// The same race for metadata: the server's map changed after the plan. The
// update sends the server's map and state holds the planned one.
func TestAlertDestinationUpdateServerFlipBetweenPlanAndApply(t *testing.T) {
	t.Parallel()
	planned := stringMap(map[string]string{"purpose": "old"})
	fake, got := runDestinationUpdate(t, planned, planned, types.MapNull(types.StringType))
	sent, _ := fake.lastPut["metadata"].(map[string]any)
	if sent["purpose"] != "k8s" || sent["render_format"] != "slack" || len(sent) != 2 {
		t.Errorf("PUT metadata: got %v, want the server's map", fake.lastPut["metadata"])
	}
	if !got.Metadata.Equal(planned) {
		t.Errorf("state metadata: got %v, want the planned %v", got.Metadata, planned)
	}
}

// A destination created in the UI from a prebuilt kind carries fields with no
// attribute. Losing them on update would leave it half converted: still
// marked prebuilt in its metadata, but no longer formatted as one.
func TestAlertDestinationUpdateKeepsUnmanagedFields(t *testing.T) {
	t.Parallel()
	for name, configured := range map[string]types.Map{
		"metadata unset":      types.MapNull(types.StringType),
		"metadata configured": stringMap(map[string]string{"prebuilt_type": "pagerduty"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, srv := newReplacingServer(t)
			stored := storedDestination(map[string]any{"prebuilt_type": "pagerduty"})
			stored["destination_type_name"] = "pagerduty"
			stored["output_format"] = "nestedevent"
			fake.put(testDestinationPath, stored)
			r := &AlertDestinationResource{client: newClient(srv.URL, "u", "p", "default")}

			planned := configured
			if planned.IsNull() {
				planned = stringMap(map[string]string{"prebuilt_type": "pagerduty"})
			}
			req, resp := updateRequest(t, r,
				destinationModel("https://hooks.example/b", planned),
				destinationModel("https://hooks.example/a", planned),
				destinationModel("https://hooks.example/b", configured),
			)
			r.Update(context.Background(), req, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %+v", resp.Diagnostics)
			}
			for k, v := range map[string]string{
				"destination_type_name": "pagerduty",
				"output_format":         "nestedevent",
			} {
				if fake.lastPut[k] != v {
					t.Errorf("PUT %s: got %v, want %q", k, fake.lastPut[k], v)
				}
			}
			if fake.lastPut["url"] != "https://hooks.example/b" {
				t.Errorf("PUT url: got %v, want the planned change", fake.lastPut["url"])
			}
		})
	}
}

// A configured map replaces the server's, and `{}` clears it and stays `{}`.
func TestAlertDestinationUpdateSendsConfiguredMetadata(t *testing.T) {
	t.Parallel()
	want := stringMap(map[string]string{"purpose": "billing"})
	fake, got := runDestinationUpdate(t, want, stringMap(map[string]string{"purpose": "k8s", "render_format": "slack"}), want)
	sent, _ := fake.lastPut["metadata"].(map[string]any)
	if len(sent) != 1 || sent["purpose"] != "billing" {
		t.Errorf("PUT metadata: got %v, want {purpose: billing}", fake.lastPut["metadata"])
	}
	if !got.Metadata.Equal(want) {
		t.Errorf("state metadata: got %v, want %v", got.Metadata, want)
	}

	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	fake, got = runDestinationUpdate(t, empty, stringMap(map[string]string{"purpose": "k8s"}), empty)
	if m, ok := fake.lastPut["metadata"].(map[string]any); ok && len(m) != 0 {
		t.Errorf("PUT metadata for {}: got %v, want nothing", m)
	}
	if !got.Metadata.Equal(empty) {
		t.Errorf("state metadata for {}: got %v, want {}", got.Metadata)
	}
}

// A `{}` left in state after the attribute was removed from the configuration
// stays `{}` across an update and a read, rather than turning null against a
// planned `{}`.
func TestAlertDestinationKeepsEmptyMetadataFromState(t *testing.T) {
	t.Parallel()
	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	fake, srv := newReplacingServer(t)
	fake.put(testDestinationPath, storedDestination(map[string]any{}))
	r := &AlertDestinationResource{client: newClient(srv.URL, "u", "p", "default")}

	req, resp := updateRequest(t, r,
		destinationModel("https://hooks.example/b", empty),
		destinationModel("https://hooks.example/a", empty),
		destinationModel("https://hooks.example/b", types.MapNull(types.StringType)),
	)
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %+v", resp.Diagnostics)
	}
	var got AlertDestinationResourceModel
	resp.State.Get(context.Background(), &got)
	if !got.Metadata.Equal(empty) {
		t.Errorf("state metadata after update: got %v, want {}", got.Metadata)
	}

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: resp.State.Schema, Raw: resp.State.Raw.Copy()}}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %+v", readResp.Diagnostics)
	}
	readResp.State.Get(context.Background(), &got)
	if !got.Metadata.Equal(empty) {
		t.Errorf("state metadata after read: got %v, want {}", got.Metadata)
	}
}

// Metadata configured on create reaches the server and reads back the same.
func TestAlertDestinationMetadataRoundTrips(t *testing.T) {
	t.Parallel()
	fake, srv := newReplacingServer(t)
	r := &AlertDestinationResource{client: newClient(srv.URL, "u", "p", "default")}
	want := stringMap(map[string]string{"purpose": "k8s", "render_format": "slack"})

	plan := tfValue(t, r, destinationModel("https://hooks.example/a", want))
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema, Raw: plan.Raw.Copy()}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(plan)}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %+v", createResp.Diagnostics)
	}
	stored, _ := fake.get(testDestinationPath)["metadata"].(map[string]any)
	if len(stored) != 2 || stored["purpose"] != "k8s" {
		t.Fatalf("server metadata after create: got %v", stored)
	}

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: plan.Schema, Raw: createResp.State.Raw.Copy()}}
	r.Read(context.Background(), resource.ReadRequest{State: createResp.State}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %+v", readResp.Diagnostics)
	}
	var got AlertDestinationResourceModel
	readResp.State.Get(context.Background(), &got)
	if !got.Metadata.Equal(want) {
		t.Errorf("metadata after read: got %v, want %v", got.Metadata, want)
	}

	// Unset on create: nothing is sent and state holds null, not unknown.
	plan = tfValue(t, r, destinationModel("https://hooks.example/a", types.MapUnknown(types.StringType)))
	createResp = &resource.CreateResponse{State: tfsdk.State{Schema: plan.Schema, Raw: plan.Raw.Copy()}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(plan)}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create unset: %+v", createResp.Diagnostics)
	}
	createResp.State.Get(context.Background(), &got)
	if !got.Metadata.IsNull() {
		t.Errorf("state metadata for an unset create: got %v, want null", got.Metadata)
	}
}

// The server keeps metadata only on webhook destinations.
func TestAlertDestinationValidateRejectsMetadataOffHTTP(t *testing.T) {
	t.Parallel()
	r := &AlertDestinationResource{}
	model := destinationModel("", stringMap(map[string]string{"purpose": "x"}))
	model.DestinationType = types.StringValue("email")
	model.Emails = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("a@example.com")})
	cfg := tfValue(t, r, model)
	resp := &resource.ValidateConfigResponse{}
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: tfsdk.Config(cfg)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want metadata on an email destination rejected")
	}
}
