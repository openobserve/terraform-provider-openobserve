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
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// fakeDowntimes stands in for /api/v2/{org}/downtimes with the rules the
// provider depends on: the server trims and lowercases condition values,
// sets duration_secs of a one-time row to its span, refuses an update naming
// a stale version, keeps the folder on update, and refuses to delete a row
// that is scheduled or active.
type fakeDowntimes struct {
	t       *testing.T
	mu      sync.Mutex
	rows    map[string]map[string]any
	nextID  int
	calls   []string
	lastPut map[string]any
	// edition answers every route with 403 "Not Supported" when "oss".
	edition string
	// sloCorrected makes a delete answer 409 as for a row that corrected SLOs.
	sloCorrected bool
	// endBeforeCancel makes a cancel answer 409 "already ended", as when the
	// window ends between the provider's read and its cancel.
	endBeforeCancel bool
	// forbidGet makes a GET by id answer 403, as for a user who may list
	// downtimes but not read one.
	forbidGet bool
	// statusOverride is the status a GET reports, by id, instead of the real one.
	statusOverride map[string]string
}

func newFakeDowntimes(t *testing.T) (*fakeDowntimes, *Client) {
	t.Helper()
	f := &fakeDowntimes{t: t, rows: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, newClient(srv.URL, "u", "p", "default")
}

func (f *fakeDowntimes) status(row map[string]any) string {
	if row["cancelled_at"] != nil {
		return "cancelled"
	}
	sched := row["schedule"].(map[string]any)
	start := int64(sched["starts_at"].(float64))
	now := time.Now().UnixMicro()
	if now < start {
		return "scheduled"
	}
	if end, ok := sched["ends_at"].(float64); ok && now >= int64(end) {
		return "ended"
	}
	return "active"
}

func normalizeFakeCondition(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if m["type"] == "pair" {
		m["key"] = strings.TrimSpace(m["key"].(string))
		m["value"] = strings.ToLower(strings.TrimSpace(m["value"].(string)))
		return m
	}
	if items, ok := m["items"].([]any); ok {
		for i := range items {
			items[i] = normalizeFakeCondition(items[i])
		}
	}
	return m
}

func (f *fakeDowntimes) store(id string, body map[string]any, version float64) map[string]any {
	body["id"] = id
	body["version"] = version
	if body["condition"] != nil {
		body["condition"] = normalizeFakeCondition(body["condition"])
	}
	sched := body["schedule"].(map[string]any)
	if sched["repeat"] == "none" {
		if end, ok := sched["ends_at"].(float64); ok {
			sched["duration_secs"] = (end - sched["starts_at"].(float64)) / 1e6
		}
	}
	delete(body, "alert_groups_sent")
	return body
}

func (f *fakeDowntimes) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/v2/default/downtimes"))
	if f.edition == "oss" {
		http.Error(w, `{"code":403,"message":"Not Supported"}`, http.StatusForbidden)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v2/default/downtimes")
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s %s: body is not JSON: %v", r.Method, r.URL.Path, err)
		}
	}
	notFound := func() { http.Error(w, `{"code":404,"message":"Downtime not found"}`, http.StatusNotFound) }
	switch {
	case r.Method == http.MethodPost && rest == "":
		f.nextID++
		id := fmt.Sprintf("dt%d", f.nextID)
		f.rows[id] = f.store(id, body, 1)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
	case r.Method == http.MethodPatch && rest == "/move":
		for _, id := range body["downtime_ids"].([]any) {
			row := f.rows[id.(string)]
			row["folder_id"] = body["dst_folder_id"]
			row["version"] = row["version"].(float64) + 1
		}
		_, _ = w.Write([]byte(`{"code":200}`))
	case strings.HasSuffix(rest, "/cancel") && r.Method == http.MethodPost:
		row, ok := f.rows[strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/cancel")]
		if !ok {
			notFound()
			return
		}
		if f.status(row) == "ended" || f.endBeforeCancel {
			http.Error(w, `{"code":409,"message":"Downtime already ended"}`, http.StatusConflict)
			return
		}
		row["cancelled_at"] = float64(time.Now().UnixMicro())
		row["version"] = row["version"].(float64) + 1
		_ = json.NewEncoder(w).Encode(row)
	default:
		id := strings.TrimPrefix(rest, "/")
		row, ok := f.rows[id]
		if !ok {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			if f.forbidGet {
				http.Error(w, "Unauthorized Access", http.StatusForbidden)
				return
			}
			out := map[string]any{}
			for k, v := range row {
				out[k] = v
			}
			out["status"] = f.status(row)
			if st, ok := f.statusOverride[id]; ok {
				out["status"] = st
			}
			out["affected"] = map[string]any{"alerts": []any{}}
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPut:
			if v, ok := body["version"].(float64); ok && v != row["version"].(float64) {
				http.Error(w, `{"code":409,"message":"Someone changed this downtime since you loaded it."}`, http.StatusConflict)
				return
			}
			f.lastPut = map[string]any{}
			for k, v := range body {
				f.lastPut[k] = v
			}
			body["folder_id"] = row["folder_id"]
			body["cancelled_at"] = row["cancelled_at"]
			f.rows[id] = f.store(id, body, row["version"].(float64)+1)
			_ = json.NewEncoder(w).Encode(f.rows[id])
		case http.MethodDelete:
			if s := f.status(row); s == "active" || s == "scheduled" {
				http.Error(w, `{"code":409,"message":"This downtime is active or scheduled. Cancel it first."}`, http.StatusConflict)
				return
			}
			if f.sloCorrected {
				http.Error(w, `{"code":409,"message":"This downtime corrected SLO slices. Cancel it instead."}`, http.StatusConflict)
				return
			}
			delete(f.rows, id)
			_, _ = w.Write([]byte(`{"code":200}`))
		default:
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		}
	}
}

// ---------------------------------------------------------------------------
// Model helpers
// ---------------------------------------------------------------------------

var downtimeTargetObjType = types.ObjectType{AttrTypes: downtimeTargetAttrTy}

func downtimeTarget(module string, ids ...string) attr.Value {
	idSet := types.SetNull(types.StringType)
	if len(ids) > 0 {
		idSet = stringSetOf(ids...)
	}
	return types.ObjectValueMust(downtimeTargetAttrTy, map[string]attr.Value{
		"module":        types.StringValue(module),
		"folder_ids":    types.SetNull(types.StringType),
		"tags":          types.SetNull(types.StringType),
		"ids":           idSet,
		"slo_mode":      types.StringNull(),
		"incident_mode": types.StringValue("muted"),
	})
}

func stringSetOf(v ...string) types.Set {
	values := make([]attr.Value, 0, len(v))
	for _, s := range v {
		values = append(values, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, values)
}

// oneTimeDowntime is a configuration of a one-time downtime from start to end.
func oneTimeDowntime(start, end string) DowntimeResourceModel {
	return DowntimeResourceModel{
		ID:         types.StringUnknown(),
		DowntimeID: types.StringUnknown(),
		OrgID:      types.StringUnknown(),
		FolderID:   types.StringValue("default"),
		Name:       types.StringValue("tf_window"),
		Reason:     types.StringNull(),
		Condition: types.StringValue(`{"type":"group","op":"and","items":[` +
			`{"type":"pair","key":"service","operator":"=","value":" Checkout "}]}`),
		ShowBanner: types.BoolValue(false),
		Status:     types.StringUnknown(),
		Targets:    types.ListValueMust(downtimeTargetObjType, []attr.Value{downtimeTarget("alerts", "a1")}),
		Schedule: &DowntimeScheduleModel{
			Repeat:         types.StringValue("none"),
			StartsAt:       types.StringValue(start),
			EndsAt:         types.StringValue(end),
			Timezone:       types.StringValue("UTC"),
			StartTimeLocal: types.StringNull(),
			DurationSecs:   types.Int64Unknown(),
			Weekdays:       types.SetNull(types.Int64Type),
		},
	}
}

func downtimeState(t *testing.T, r *DowntimeResource, s tfsdk.State) DowntimeResourceModel {
	t.Helper()
	var m DowntimeResourceModel
	if d := s.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("decode state: %v", d)
	}
	return m
}

func createDowntime(t *testing.T, r *DowntimeResource, plan DowntimeResourceModel) (DowntimeResourceModel, diag.Diagnostics) {
	t.Helper()
	p := tfValue(t, r, plan)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(p), Config: tfsdk.Config(p)}, resp)
	if resp.Diagnostics.HasError() {
		return DowntimeResourceModel{}, resp.Diagnostics
	}
	return downtimeState(t, r, resp.State), resp.Diagnostics
}

func future(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// A created downtime reads back as configured: the timestamps written with an
// offset, and the condition written with spaces and capitals, which the
// server trims and lowercases.
func TestDowntimeCreateReadsBackAsConfigured(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime("2027-01-10T02:00:00+05:30", "2027-01-10T04:00:00+05:30")
	plan.Schedule.DurationSecs = types.Int64Value(7200)

	got, diags := createDowntime(t, r, plan)
	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	if got.Schedule.StartsAt.ValueString() != "2027-01-10T02:00:00+05:30" {
		t.Errorf("starts_at: got %v, want the configured string", got.Schedule.StartsAt)
	}
	if !got.Condition.Equal(plan.Condition) {
		t.Errorf("condition: got %v, want the configured string", got.Condition)
	}
	if got.Schedule.DurationSecs.ValueInt64() != 7200 {
		t.Errorf("duration_secs: got %v", got.Schedule.DurationSecs)
	}
	if got.ID.ValueString() != "default/dt1" || got.Status.ValueString() != "scheduled" {
		t.Errorf("id/status: got %v %v", got.ID, got.Status)
	}
	stored := fake.rows["dt1"]
	cond := stored["condition"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if cond["value"] != "checkout" || cond["key"] != "service" {
		t.Errorf("fake did not normalize: %v", cond)
	}
	if stored["schedule"].(map[string]any)["starts_at"] != float64(time.Date(2027, 1, 9, 20, 30, 0, 0, time.UTC).UnixMicro()) {
		t.Errorf("starts_at sent: got %v", stored["schedule"].(map[string]any)["starts_at"])
	}
	targets := stored["targets"].([]any)
	if f := targets[0].(map[string]any)["folders"].(map[string]any); f["kind"] != "all" {
		t.Errorf("folders: got %v, want all", f)
	}
}

// An update sends the stored version and the quick-mute fields back, keeps
// the folder in the PUT, and moves the downtime when folder_id changed.
func TestDowntimeUpdateSendsVersionAndMoves(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(24*time.Hour), future(26*time.Hour))
	plan.Schedule.DurationSecs = types.Int64Value(7200)
	state, diags := createDowntime(t, r, plan)
	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	// Edited in the UI since: the version moved on and a quick mute added groups.
	fake.rows["dt1"]["version"] = float64(4)
	fake.rows["dt1"]["alert_groups"] = []any{map[string]any{"alert_id": "a1", "group_key": "k1"}}

	next := state
	next.Reason = types.StringValue("db upgrade")
	next.FolderID = types.StringValue("maint")
	next.Status = types.StringUnknown()
	req, resp := updateRequest(t, r, next, state, next)
	r.Update(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if fake.lastPut["version"] != float64(4) {
		t.Errorf("PUT version: got %v, want 4", fake.lastPut["version"])
	}
	if fake.lastPut["folder_id"] != "default" {
		t.Errorf("PUT folder_id: got %v, want the current folder; the move changes it", fake.lastPut["folder_id"])
	}
	if groups, _ := fake.lastPut["alert_groups"].([]any); len(groups) != 1 {
		t.Errorf("PUT alert_groups: got %v, want the server's", fake.lastPut["alert_groups"])
	}
	got := downtimeState(t, r, resp.State)
	if got.FolderID.ValueString() != "maint" || got.Reason.ValueString() != "db upgrade" {
		t.Errorf("state: folder %v reason %v", got.FolderID, got.Reason)
	}
	if !strings.Contains(strings.Join(fake.calls, ","), "PATCH /move") {
		t.Errorf("no move call: %v", fake.calls)
	}
}

// Destroying a scheduled downtime cancels it first; an ended one is deleted
// directly; one that corrected SLO slices is cancelled and left with a warning.
func TestDowntimeDelete(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		start, end   time.Duration
		sloCorrected bool
		wantCancel   bool
		wantGone     bool
		wantWarning  bool
	}{
		"scheduled":     {time.Hour, 2 * time.Hour, false, true, true, false},
		"active":        {-time.Minute, time.Hour, false, true, true, false},
		"ended":         {-2 * time.Hour, -time.Hour, false, false, true, false},
		"slo-corrected": {-2 * time.Hour, -time.Hour, true, false, false, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake, client := newFakeDowntimes(t)
			fake.sloCorrected = tc.sloCorrected
			r := &DowntimeResource{client: client}
			plan := oneTimeDowntime(future(tc.start), future(tc.end))
			plan.Schedule.DurationSecs = types.Int64Value(int64((tc.end - tc.start) / time.Second))
			state, diags := createDowntime(t, r, plan)
			if diags.HasError() {
				t.Fatalf("create: %v", diags)
			}
			st := tfValue(t, r, state)
			resp := &resource.DeleteResponse{State: st}
			r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("delete: %v", resp.Diagnostics)
			}
			calls := strings.Join(fake.calls, ",")
			if got := strings.Contains(calls, "/cancel"); got != tc.wantCancel {
				t.Errorf("cancel called: %v, want %v (%s)", got, tc.wantCancel, calls)
			}
			if _, present := fake.rows["dt1"]; present == tc.wantGone {
				t.Errorf("row present after delete: %v", present)
			}
			if got := resp.Diagnostics.WarningsCount() > 0; got != tc.wantWarning {
				t.Errorf("warning: %v, want %v", got, tc.wantWarning)
			}
		})
	}
}

// A downtime deleted outside Terraform leaves state; a destroy of one is a no-op.
func TestDowntimeReadRemovesMissing(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(time.Hour), future(2*time.Hour))
	plan.Schedule.DurationSecs = types.Int64Value(3600)
	state, diags := createDowntime(t, r, plan)
	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	delete(fake.rows, "dt1")
	st := tfValue(t, r, state)
	resp := &resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Errorf("read: diags %v, state null %v", resp.Diagnostics, resp.State.Raw.IsNull())
	}
}

// An open-source server answers 403 "Not Supported"; the diagnostic says
// downtimes need Enterprise.
func TestDowntimeOnOpenSource(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	fake.edition = "oss"
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(time.Hour), future(2*time.Hour))
	plan.Schedule.DurationSecs = types.Int64Value(3600)
	_, diags := createDowntime(t, r, plan)
	if !diags.HasError() || !strings.Contains(diags.Errors()[0].Summary(), "Enterprise") {
		t.Errorf("want an Enterprise diagnostic, got %v", diags)
	}
}

func TestDowntimeImport(t *testing.T) {
	t.Parallel()
	_, client := newFakeDowntimes(t)
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(time.Hour), future(2*time.Hour))
	plan.Schedule.DurationSecs = types.Int64Value(3600)
	if _, diags := createDowntime(t, r, plan); diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	for _, id := range []string{"default/dt1", "dt1"} {
		p := tfValue(t, r, plan)
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("import %s: %v", id, resp.Diagnostics)
		}
		got := downtimeState(t, r, resp.State)
		if got.ID.ValueString() != "default/dt1" || got.Name.ValueString() != "tf_window" ||
			got.Schedule == nil || got.Schedule.Repeat.ValueString() != "none" {
			t.Errorf("import %s: got %+v", id, got)
		}
		cond, err := parseDowntimeCondition(got.Condition.ValueString())
		if err != nil || *cond.Items[0].Value != "checkout" {
			t.Errorf("import %s condition: %v %v", id, got.Condition, err)
		}
	}
}

func TestDowntimeValidateConfig(t *testing.T) {
	t.Parallel()
	recurring := func(m *DowntimeResourceModel, repeat string) {
		m.Schedule.Repeat = types.StringValue(repeat)
		m.Schedule.EndsAt = types.StringNull()
		m.Schedule.StartTimeLocal = types.StringValue("02:00")
		m.Schedule.DurationSecs = types.Int64Value(3600)
		m.Schedule.Weekdays = types.SetNull(types.Int64Type)
		if repeat == "weekly" {
			m.Schedule.Weekdays = types.SetValueMust(types.Int64Type, []attr.Value{types.Int64Value(6), types.Int64Value(7)})
		}
	}
	cases := map[string]struct {
		edit    func(m *DowntimeResourceModel)
		wantErr string
	}{
		"one-time":    {func(m *DowntimeResourceModel) {}, ""},
		"no schedule": {func(m *DowntimeResourceModel) { m.Schedule = nil }, "Missing schedule"},
		"no ends_at":  {func(m *DowntimeResourceModel) { m.Schedule.EndsAt = types.StringNull() }, "needs `ends_at`"},
		"end before start": {func(m *DowntimeResourceModel) {
			m.Schedule.EndsAt = types.StringValue("2027-01-01T00:00:00Z")
		}, "after `starts_at`"},
		"window too short": {func(m *DowntimeResourceModel) {
			m.Schedule.EndsAt = types.StringValue("2027-01-10T00:00:30Z")
		}, "between 1 minute and 7 days"},
		"window too long": {func(m *DowntimeResourceModel) {
			m.Schedule.EndsAt = types.StringValue("2027-01-20T00:00:00Z")
		}, "between 1 minute and 7 days"},
		"one-time with duration": {func(m *DowntimeResourceModel) {
			m.Schedule.DurationSecs = types.Int64Value(60)
		}, "set by the window"},
		"one-time with weekdays": {func(m *DowntimeResourceModel) {
			m.Schedule.Weekdays = types.SetValueMust(types.Int64Type, []attr.Value{types.Int64Value(1)})
		}, "recurring downtimes only"},
		"daily":  {func(m *DowntimeResourceModel) { recurring(m, "daily") }, ""},
		"weekly": {func(m *DowntimeResourceModel) { recurring(m, "weekly") }, ""},
		"daily without start time": {func(m *DowntimeResourceModel) {
			recurring(m, "daily")
			m.Schedule.StartTimeLocal = types.StringNull()
		}, "start_time_local"},
		"daily with weekdays": {func(m *DowntimeResourceModel) {
			recurring(m, "daily")
			m.Schedule.Weekdays = types.SetValueMust(types.Int64Type, []attr.Value{types.Int64Value(1)})
		}, "weekly downtimes only"},
		"daily longer than a day": {func(m *DowntimeResourceModel) {
			recurring(m, "daily")
			m.Schedule.DurationSecs = types.Int64Value(90000)
		}, "between repeats"},
		"weekly without weekdays": {func(m *DowntimeResourceModel) {
			recurring(m, "weekly")
			m.Schedule.Weekdays = types.SetNull(types.Int64Type)
		}, "Missing weekdays"},
		"weekly window longer than the gap": {func(m *DowntimeResourceModel) {
			recurring(m, "weekly")
			m.Schedule.DurationSecs = types.Int64Value(2 * 86400)
		}, "between repeats"},
		"no target": {func(m *DowntimeResourceModel) {
			m.Targets = types.ListValueMust(downtimeTargetObjType, []attr.Value{})
		}, "Missing target"},
		"module twice": {func(m *DowntimeResourceModel) {
			m.Targets = types.ListValueMust(downtimeTargetObjType, []attr.Value{downtimeTarget("alerts"), downtimeTarget("alerts")})
		}, "chosen twice"},
		"tags on alerts": {func(m *DowntimeResourceModel) {
			obj := downtimeTarget("alerts").(types.Object).Attributes()
			obj["tags"] = stringSetOf("prod")
			m.Targets = types.ListValueMust(downtimeTargetObjType, []attr.Value{types.ObjectValueMust(downtimeTargetAttrTy, obj)})
		}, "synthetics checks only"},
		"condition on synthetics only": {func(m *DowntimeResourceModel) {
			m.Targets = types.ListValueMust(downtimeTargetObjType, []attr.Value{downtimeTarget("synthetics")})
		}, "nothing to narrow"},
		"bad condition": {func(m *DowntimeResourceModel) {
			m.Condition = types.StringValue(`{"type":"pair","key":"service","operator":"~","value":"x"}`)
		}, "must be `=` or `!=`"},
		"unknown condition field": {func(m *DowntimeResourceModel) {
			m.Condition = types.StringValue(`{"type":"pair","key":"service","operator":"=","value":"x","oops":1}`)
		}, "not a valid condition"},
		"key with spaces": {func(m *DowntimeResourceModel) {
			m.Condition = types.StringValue(`{"type":"pair","key":" service ","operator":"=","value":"x"}`)
		}, "surrounding spaces"},
		"default reminder longer than the window": {func(m *DowntimeResourceModel) {
			m.Schedule.EndsAt = types.StringValue("2027-01-10T00:05:00Z")
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: stringSetOf("ending_soon"),
				EndingSoonLeadSecs: types.Int64Null(),
			}
		}, "600 when unset"},
		"full-day recurring reminder": {func(m *DowntimeResourceModel) {
			recurring(m, "daily")
			m.Schedule.DurationSecs = types.Int64Value(86400)
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: stringSetOf("ending_soon"),
				EndingSoonLeadSecs: types.Int64Value(86000),
			}
		}, "an hour shorter"},
		"full-day recurring reminder that fits": {func(m *DowntimeResourceModel) {
			recurring(m, "daily")
			m.Schedule.DurationSecs = types.Int64Value(86400)
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: stringSetOf("ending_soon"),
				EndingSoonLeadSecs: types.Int64Value(82800),
			}
		}, ""},
		"notifications without events": {func(m *DowntimeResourceModel) {
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: types.SetNull(types.StringType),
				EndingSoonLeadSecs: types.Int64Value(600),
			}
		}, "Missing events"},
		"reminder longer than the window": {func(m *DowntimeResourceModel) {
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: stringSetOf("ending_soon"),
				EndingSoonLeadSecs: types.Int64Value(9000),
			}
		}, "longer than the window"},
		"reminder fits": {func(m *DowntimeResourceModel) {
			m.Notifications = &DowntimeNotificationsModel{
				Destinations: stringSetOf("d1"), Events: stringSetOf("ending_soon", "started"),
				EndingSoonLeadSecs: types.Int64Value(600),
			}
		}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := oneTimeDowntime("2027-01-10T00:00:00Z", "2027-01-10T02:00:00Z")
			m.Schedule.DurationSecs = types.Int64Null()
			tc.edit(&m)
			var diags diag.Diagnostics
			validateDowntimeConfig(context.Background(), &m, &diags)
			got := ""
			for _, d := range diags.Errors() {
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

func TestDowntimeConditionToModel(t *testing.T) {
	t.Parallel()
	server := json.RawMessage(`{"type":"pair","key":"env","operator":"=","value":"prod"}`)
	var d diag.Diagnostics
	same := types.StringValue(`{ "type": "pair", "key": "env", "operator": "=", "value": " PROD " }`)
	if got := downtimeConditionToModel(same, server, &d); !got.Equal(same) {
		t.Errorf("same after normalization: got %v", got)
	}
	other := types.StringValue(`{"type":"pair","key":"env","operator":"=","value":"staging"}`)
	if got := downtimeConditionToModel(other, server, &d); got.Equal(other) || !strings.Contains(got.ValueString(), `"prod"`) {
		t.Errorf("drift: got %v, want the server's", got)
	}
	imported := downtimeConditionToModel(types.StringNull(),
		json.RawMessage(`{"type":"group","op":"and","items":[{"type":"pair","key":"env","operator":"=","value":"prod"}]}`), &d)
	if want := `{"items":[{"key":"env","operator":"=","type":"pair","value":"prod"}],"op":"and","type":"group"}`; imported.ValueString() != want {
		t.Errorf("import: got %s, want the jsonencode() form %s", imported.ValueString(), want)
	}
	if got := downtimeConditionToModel(types.StringNull(), nil, &d); !got.IsNull() {
		t.Errorf("none: got %v", got)
	}
}

func TestTimestampToModel(t *testing.T) {
	t.Parallel()
	at := time.Date(2027, 1, 9, 20, 30, 0, 0, time.UTC).UnixMicro()
	if got := timestampToModel(types.StringValue("2027-01-10T02:00:00+05:30"), at); got.ValueString() != "2027-01-10T02:00:00+05:30" {
		t.Errorf("same instant: got %v", got)
	}
	if got := timestampToModel(types.StringNull(), at); got.ValueString() != "2027-01-09T20:30:00Z" {
		t.Errorf("import: got %v", got)
	}
	if got := timestampToModel(types.StringValue("2027-01-10T03:00:00+05:30"), at); got.ValueString() != "2027-01-09T20:30:00Z" {
		t.Errorf("drift: got %v", got)
	}
}

func TestWeeklyGapSecs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		days []int64
		want int64
	}{
		{[]int64{1}, 7 * 86400},
		{[]int64{6, 7}, 86400},
		{[]int64{1, 4}, 3 * 86400},
		{[]int64{7, 1, 1}, 86400},
	} {
		if got := weeklyGapSecs(tc.days); got != tc.want {
			t.Errorf("%v: got %d, want %d", tc.days, got, tc.want)
		}
	}
}

// A finished downtime the server's retention deleted stays in state, so it is
// not created again; an unfinished one that vanished is removed.
func TestDowntimeReadKeepsSweptFinishedDowntime(t *testing.T) {
	t.Parallel()
	for status, keep := range map[string]bool{"ended": true, "cancelled": true, "ended_early": true, "scheduled": false} {
		fake, client := newFakeDowntimes(t)
		r := &DowntimeResource{client: client}
		plan := oneTimeDowntime(future(time.Hour), future(2*time.Hour))
		plan.Schedule.DurationSecs = types.Int64Value(3600)
		state, diags := createDowntime(t, r, plan)
		if diags.HasError() {
			t.Fatalf("create: %v", diags)
		}
		state.Status = types.StringValue(status)
		delete(fake.rows, "dt1")
		st := tfValue(t, r, state)
		resp := &resource.ReadResponse{State: st}
		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: read: %v", status, resp.Diagnostics)
		}
		if kept := !resp.State.Raw.IsNull(); kept != keep {
			t.Errorf("%s: kept in state %v, want %v", status, kept, keep)
		}
	}
}

// A window that ends between the read and the cancel is deleted anyway.
func TestDowntimeDeleteWhenWindowEndsBeforeCancel(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(-time.Minute), future(time.Hour))
	plan.Schedule.DurationSecs = types.Int64Value(3660)
	state, diags := createDowntime(t, r, plan)
	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	// The read still says active; by the cancel, the window has ended.
	fake.statusOverride = map[string]string{"dt1": "active"}
	fake.endBeforeCancel = true
	fake.rows["dt1"]["schedule"].(map[string]any)["ends_at"] = float64(time.Now().Add(-time.Second).UnixMicro())
	st := tfValue(t, r, state)
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if _, present := fake.rows["dt1"]; present {
		t.Error("row still present")
	}
}

func TestSameDowntimeConditionUnicode(t *testing.T) {
	t.Parallel()
	pair := func(v string) downtimeCondition {
		return downtimeCondition{Type: "pair", Key: "city", Operator: "=", Value: &v}
	}
	for _, tc := range []struct{ configured, server string }{
		{"ΟΔΟΣ", "οδος"},
		{"İSTANBUL", "i\u0307stanbul"},
		{"Checkout", "checkout"},
	} {
		if !sameDowntimeCondition(normalizedDowntimeCondition(pair(tc.configured)), pair(tc.server)) {
			t.Errorf("%q vs server %q: want equal", tc.configured, tc.server)
		}
	}
	if sameDowntimeCondition(pair("prod"), pair("staging")) {
		t.Error("different values must differ")
	}
}

// A create whose read-back fails still records the downtime, so it is not
// left on the server untracked.
func TestDowntimeCreateKeepsStateWhenReadFails(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	fake.forbidGet = true
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(time.Hour), future(2*time.Hour))
	p := tfValue(t, r, plan)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: p.Schema, Raw: tftypes.NewValue(p.Schema.Type().TerraformType(context.Background()), nil)}}
	r.Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan(p), Config: tfsdk.Config(p)}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the read error")
	}
	got := downtimeState(t, r, resp.State)
	if got.DowntimeID.ValueString() != "dt1" || got.ID.ValueString() != "default/dt1" {
		t.Errorf("state: got id %v / %v, want the created downtime", got.ID, got.DowntimeID)
	}
}

// A downtime the server keeps because it corrected SLOs is moved to the
// default folder, so its own folder can be deleted.
func TestDowntimeDeleteSloCorrectedMovesToDefault(t *testing.T) {
	t.Parallel()
	fake, client := newFakeDowntimes(t)
	fake.sloCorrected = true
	r := &DowntimeResource{client: client}
	plan := oneTimeDowntime(future(-2*time.Hour), future(-time.Hour))
	plan.FolderID = types.StringValue("maint")
	plan.Schedule.DurationSecs = types.Int64Value(3600)
	state, diags := createDowntime(t, r, plan)
	if diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	st := tfValue(t, r, state)
	resp := &resource.DeleteResponse{State: st}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, resp)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if got := fake.rows["dt1"]["folder_id"]; got != "default" {
		t.Errorf("folder after delete: got %v, want default", got)
	}
}

// The span is computed at the server's microsecond precision.
func TestDowntimeSpanAtMicroseconds(t *testing.T) {
	t.Parallel()
	m := oneTimeDowntime("2026-12-01T22:00:00.0000005Z", "2026-12-01T22:02:00Z")
	m.Schedule.DurationSecs = types.Int64Null()
	var diags diag.Diagnostics
	if span, ok := validateDowntimeSchedule(m.Schedule, &diags); !ok || span != 120 || diags.HasError() {
		t.Errorf("span: got %d %v %v, want 120", span, ok, diags)
	}
	tiny := oneTimeDowntime("2026-12-01T22:00:00.0000001Z", "2026-12-01T22:00:00.0000009Z")
	tiny.Schedule.DurationSecs = types.Int64Null()
	diags = nil
	validateDowntimeSchedule(tiny.Schedule, &diags)
	if !diags.HasError() {
		t.Error("an end within the same microsecond must be refused")
	}
}
