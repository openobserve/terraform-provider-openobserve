package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// v2Configured is a v2 condition node as jsonencode() renders it: keys sorted,
// `Contains` in the PascalCase spelling, and no ignore_case on the first item.
// It reads `level contains err AND (code = 500 OR code = 503)`.
const v2Configured = `{"conditions":{"conditions":[` +
	`{"column":"level","filterType":"condition","logicalOperator":"AND","operator":"Contains","value":"err"},` +
	`{"conditions":[` +
	`{"column":"code","filterType":"condition","logicalOperator":"AND","operator":"=","value":500},` +
	`{"column":"code","filterType":"condition","logicalOperator":"OR","operator":"=","value":503}],` +
	`"filterType":"group","logicalOperator":"AND"}],` +
	`"filterType":"group","logicalOperator":"AND"},"version":2}`

// v2Returned is what OpenObserve stores for v2Configured, captured from a live
// server: the group alone in `conditions`, keys in struct order, and the
// operator rewritten to snake_case. The version comes back as its own field.
const v2Returned = `{"filterType":"group","logicalOperator":"AND","conditions":[` +
	`{"filterType":"condition","column":"level","operator":"contains","value":"err","logicalOperator":"AND"},` +
	`{"filterType":"group","logicalOperator":"AND","conditions":[` +
	`{"filterType":"condition","column":"code","operator":"=","value":500,"logicalOperator":"AND"},` +
	`{"filterType":"condition","column":"code","operator":"=","value":503,"logicalOperator":"OR"}]}]}`

func conditionNodeModel(prior types.String) *PipelineResourceModel {
	return &PipelineResourceModel{Nodes: []PipelineNodeModel{{
		ID:         types.StringValue("filter"),
		Type:       types.StringValue("condition"),
		Conditions: prior,
	}}}
}

func conditionNodeAPI(version *int64, returned string) *PipelineAPI {
	return &PipelineAPI{Name: "p", Nodes: []PipelineNodeAPI{{
		ID:     "filter",
		IOType: "default",
		Data: PipelineNodeDataAPI{
			NodeType:   "condition",
			Version:    version,
			Conditions: json.RawMessage(returned),
		},
	}}}
}

func applyConditionNode(t *testing.T, prior types.String, version *int64, returned string) types.String {
	t.Helper()
	model := conditionNodeModel(prior)
	var diags diag.Diagnostics
	(&PipelineResource{}).applyToModel(context.Background(), conditionNodeAPI(version, returned), model, &diags)
	if diags.HasError() {
		t.Fatalf("applyToModel: %v", diags.Errors())
	}
	if len(model.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(model.Nodes))
	}
	return model.Nodes[0].Conditions
}

func int64Ptr(v int64) *int64 { return &v }

// TestApplyPipelineToModelKeepsConfiguredV2Conditions covers the second part
// of openobserve/terraform-provider-openobserve#11. The server returns a v2
// node's version and group as separate fields, reorders keys and re-spells
// operators. None of that changes the filter, so state keeps the configured
// text, or the apply fails with an inconsistent result.
func TestApplyPipelineToModelKeepsConfiguredV2Conditions(t *testing.T) {
	got := applyConditionNode(t, types.StringValue(v2Configured), int64Ptr(2), v2Returned)
	if got.ValueString() != v2Configured {
		t.Errorf("conditions = %s, want the configured text %s", got.ValueString(), v2Configured)
	}
}

// TestApplyPipelineToModelReportsV2Drift checks that a v2 change made outside
// Terraform still surfaces, in the wrapped form the configuration uses.
func TestApplyPipelineToModelReportsV2Drift(t *testing.T) {
	changed := strings.Replace(v2Returned, `"value":503`, `"value":504`, 1)
	got := applyConditionNode(t, types.StringValue(v2Configured), int64Ptr(2), changed)
	if got.ValueString() == v2Configured {
		t.Fatal("a changed value was hidden behind the configured text")
	}
	assertV2Wrapped(t, got.ValueString(), changed)
}

// TestApplyPipelineToModelImportsV2Conditions covers a read with nothing
// configured (import): the document is wrapped so it can be pasted back into
// a configuration as is.
func TestApplyPipelineToModelImportsV2Conditions(t *testing.T) {
	got := applyConditionNode(t, types.StringNull(), int64Ptr(2), v2Returned)
	assertV2Wrapped(t, got.ValueString(), v2Returned)
}

// TestApplyPipelineToModelV2NotConfusedWithV1 checks that a node switched
// from v1 to v2 outside Terraform shows as a change rather than matching.
func TestApplyPipelineToModelV2NotConfusedWithV1(t *testing.T) {
	const v1 = `{"and":[{"column":"level","operator":"=","value":"err"}]}`
	group := `{"filterType":"group","logicalOperator":"AND","conditions":[` +
		`{"filterType":"condition","column":"level","operator":"=","value":"err","logicalOperator":"AND"}]}`
	got := applyConditionNode(t, types.StringValue(v1), int64Ptr(2), group)
	if got.ValueString() == v1 {
		t.Fatal("a v2 node was reported as the configured v1 document")
	}
	assertV2Wrapped(t, got.ValueString(), group)
}

func assertV2Wrapped(t *testing.T, state, group string) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal([]byte(state), &got); err != nil {
		t.Fatalf("unmarshal state %s: %v", state, err)
	}
	if err := json.Unmarshal([]byte(`{"version":2,"conditions":`+group+`}`), &want); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conditions = %s, want the server's group wrapped with version 2", state)
	}
}

// TestPipelineConditionNodeWireFormat checks what goes to the server: a v2
// document is split into the node's `version` and `conditions` fields, and a
// v1 document is sent exactly as before, with no `version` at all.
func TestPipelineConditionNodeWireFormat(t *testing.T) {
	const legacy = `[{"column":"body","ignore_case":false,"operator":"=","value":"keep"}]`
	const tree = `{"or":[{"column":"a","operator":"=","value":"1"},{"column":"b","operator":"=","value":"2"}]}`
	const versionOne = `{"version":1,"conditions":[{"column":"a","operator":"=","value":"1"}]}`

	for _, tc := range []struct {
		name, configured string
		wantVersion      bool
		wantConditions   string
	}{
		{"v1 legacy list", legacy, false, legacy},
		{"v1 tree", tree, false, tree},
		{"v2 group", v2Configured, true, ""},
		// Only reachable when the document was unknown at plan time. Splitting
		// it would store v1 and read back without the wrapper.
		{"version 1 is not split", versionOne, false, versionOne},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := pipelineModel("d", types.Int64Value(0))
			model.Nodes = append(model.Nodes[:1], PipelineNodeModel{
				ID: types.StringValue("filter"), Type: types.StringValue("condition"),
				Conditions: types.StringValue(tc.configured),
			}, model.Nodes[1])

			body, err := json.Marshal((&PipelineResource{}).apiFromModel(&model, "default"))
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Nodes []struct {
					Data map[string]json.RawMessage `json:"data"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			data := wire.Nodes[1].Data

			version, hasVersion := data["version"]
			if hasVersion != tc.wantVersion {
				t.Fatalf("node data %v: version present = %v, want %v", data, hasVersion, tc.wantVersion)
			}
			if tc.wantVersion {
				if string(version) != "2" {
					t.Errorf("version = %s, want 2", version)
				}
				var gotGroup, wantGroup any
				_ = json.Unmarshal(data["conditions"], &gotGroup)
				var wrapper map[string]any
				_ = json.Unmarshal([]byte(tc.configured), &wrapper)
				wantGroup = wrapper["conditions"]
				if !reflect.DeepEqual(gotGroup, wantGroup) {
					t.Errorf("conditions = %s, want the group alone", data["conditions"])
				}
				return
			}
			if string(data["conditions"]) != tc.wantConditions {
				t.Errorf("conditions = %s, want them byte for byte as configured: %s",
					data["conditions"], tc.wantConditions)
			}
		})
	}
}

// TestPipelineUpdateRoundTripsV2Conditions runs an Update against a server
// that stores the body and returns it, and checks the version reaches the
// server and the state afterwards is the configured document.
func TestPipelineUpdateRoundTripsV2Conditions(t *testing.T) {
	ctx := context.Background()
	r := &PipelineResource{}

	withV2 := func(m PipelineResourceModel) PipelineResourceModel {
		m.Nodes = []PipelineNodeModel{
			{ID: types.StringValue("source"), Type: types.StringValue("stream"), StreamName: types.StringValue("in")},
			{ID: types.StringValue("filter"), Type: types.StringValue("condition"), Conditions: types.StringValue(v2Configured)},
			{ID: types.StringValue("destination"), Type: types.StringValue("stream"), StreamName: types.StringValue("out")},
		}
		m.Edges = []PipelineEdgeModel{
			{From: types.StringValue("source"), To: types.StringValue("filter")},
			{From: types.StringValue("filter"), To: types.StringValue("destination")},
		}
		return m
	}

	stored := withV2(pipelineModel("a", types.Int64Value(0)))
	srv := versionedPipelineServer(t, r.apiFromModel(&stored, "default"))
	r.client = newClient(srv.URL, "u", "p", "default")

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	prior := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	if diags := prior.Set(ctx, stored); diags.HasError() {
		t.Fatalf("building state: %v", diags)
	}
	plan := tfsdk.Plan{Schema: prior.Schema, Raw: tftypes.NewValue(prior.Schema.Type().TerraformType(ctx), nil)}
	if diags := plan.Set(ctx, withV2(pipelineModel("b", types.Int64Unknown()))); diags.HasError() {
		t.Fatalf("building plan: %v", diags)
	}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: prior.Schema, Raw: prior.Raw.Copy()}}
	r.Update(ctx, resource.UpdateRequest{Plan: plan, State: prior}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}

	stateNodes, err := r.client.GetPipeline(ctx, "default", "p1")
	if err != nil || stateNodes == nil {
		t.Fatalf("reading stored pipeline: %v", err)
	}
	if v := stateNodes.Nodes[1].Data.Version; v == nil || *v != 2 {
		t.Errorf("stored node version = %v, want 2", v)
	}

	var got PipelineResourceModel
	if diags := resp.State.Get(ctx, &got); diags.HasError() {
		t.Fatalf("reading state: %v", diags)
	}
	if c := got.Nodes[1].Conditions.ValueString(); c != v2Configured {
		t.Errorf("conditions in state = %s, want the configured text", c)
	}
}

func TestPipelineConditionsProblem(t *testing.T) {
	cond := func(extra string) string {
		return `{"filterType":"condition","column":"a","operator":"=","value":"1","logicalOperator":"AND"` + extra + `}`
	}
	v2 := func(items ...string) string {
		return `{"version":2,"conditions":{"filterType":"group","logicalOperator":"AND","conditions":[` +
			strings.Join(items, ",") + `]}}`
	}

	for _, tc := range []struct {
		name, doc string
		want      string // a fragment of the problem, or "" for none
	}{
		{"v1 legacy list", `[{"column":"a","operator":"=","value":"1"}]`, ""},
		{"v1 and tree", `{"and":[{"column":"a","operator":"=","value":"1"}]}`, ""},
		{"v1 single condition", `{"column":"a","operator":"=","value":"1"}`, ""},
		{"v2 nested", v2Configured, ""},
		{"v2 pascal and snake operators", v2(cond(`,"ignore_case":true`),
			strings.Replace(cond(""), `"="`, `"NotContains"`, 1),
			strings.Replace(cond(""), `"="`, `"is_not_null"`, 1)), ""},
		{"not json", `{"version":`, "not valid JSON"},
		{"v2 group without a version", `{"filterType":"group","logicalOperator":"AND","conditions":[` + cond("") + `]}`,
			"not marked as v2"},
		{"version 1", `{"version":1,"conditions":[{"column":"a","operator":"=","value":"1"}]}`, "only version"},
		{"version as a string", `{"version":"2","conditions":{}}`, "only version"},
		{"version 3", `{"version":3,"conditions":{}}`, "only version"},
		{"version 2 with a v1 list", `{"version":2,"conditions":[{"column":"a","operator":"=","value":"1"}]}`,
			"looks like a v1 document"},
		{"version 2 with a v1 tree", `{"version":2,"conditions":{"and":[{"column":"a","operator":"=","value":"1"}]}}`,
			"looks like a v1 document"},
		{"v1 node mixed into a v2 group", v2(`{"and":[{"column":"a","operator":"=","value":"1"}]}`),
			"cannot be mixed"},
		{"extra wrapper key", `{"version":2,"conditions":{"filterType":"group","logicalOperator":"AND",` +
			`"conditions":[` + cond("") + `]},"node_type":"condition"}`, "`node_type`"},
		{"empty group", v2(), "at least one condition"},
		{"group conditions not a list", `{"version":2,"conditions":{"filterType":"group","logicalOperator":"AND",` +
			`"conditions":{}}}`, "must be a list"},
		{"null logical operator", v2(strings.Replace(cond(""), `"AND"`, `null`, 1)), "got null"},
		{"lower case logical operator", v2(strings.Replace(cond(""), `"AND"`, `"and"`, 1)), "upper case"},
		{"missing logical operator", v2(`{"filterType":"condition","column":"a","operator":"=","value":"1"}`),
			"logicalOperator` is missing"},
		{"missing column", v2(`{"filterType":"condition","operator":"=","value":"1","logicalOperator":"AND"}`),
			"needs a `column`"},
		{"unknown operator", v2(strings.Replace(cond(""), `"="`, `"=="`, 1)), "`conditions.conditions[0].operator`"},
		{"version 2.0", `{"version":2.0,"conditions":{"filterType":"group","logicalOperator":"AND","conditions":[` +
			cond("") + `]}}`, "only version"},
		{"camelCase ignoreCase", v2(cond(`,"ignoreCase":true`)), "`ignoreCase`"},
		{"null ignore_case", v2(cond(`,"ignore_case":null`)), "true or false"},
		{"unknown key on a nested group", v2(`{"filterType":"group","logicalOperator":"OR","label":"x","conditions":[` +
			cond("") + `]}`), "`label`"},
		{"unknown key on the top group", `{"version":2,"conditions":{"filterType":"group","logicalOperator":"AND",` +
			`"name":"x","conditions":[` + cond("") + `]}}`, "`name`"},
		{"missing value", v2(`{"filterType":"condition","column":"a","operator":"is_null","logicalOperator":"AND"}`),
			"needs a `value`"},
		{"unary operator with empty value", v2(`{"filterType":"condition","column":"a","operator":"is_null",` +
			`"value":"","logicalOperator":"AND"}`), ""},
		{"empty column is a server-side draft", v2(`{"filterType":"condition","column":"","operator":"=",` +
			`"value":"","logicalOperator":"AND"}`), ""},
		{"bad nested group", v2(cond(""), `{"filterType":"group","logicalOperator":"OR","conditions":[{"filterType":"x"}]}`),
			"`conditions.conditions[1].conditions[0].filterType`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pipelineConditionsProblem(tc.doc)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("unexpected problem: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("problem = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestPipelineValidateConfigConditions checks the plan-time checks reach the
// right attribute: a bad v2 document on a condition node, and conditions on a
// node that would drop them.
func TestPipelineValidateConfigConditions(t *testing.T) {
	withNode := func(nodeType, conditions string) PipelineResourceModel {
		m := metricsPipeline()
		node := m.Nodes[1]
		node.Type = types.StringValue(nodeType)
		node.Conditions = types.StringValue(conditions)
		if nodeType == "condition" {
			node.StreamName = types.StringNull()
		}
		m.Nodes = []PipelineNodeModel{m.Nodes[0], node, {
			ID: types.StringValue("dst"), Type: types.StringValue("stream"), IOType: types.StringNull(),
			PositionX: types.Float64Null(), PositionY: types.Float64Null(),
			StreamName: types.StringValue("cpu_copy"), StreamType: types.StringNull(),
			FunctionName: types.StringNull(), AfterFlatten: types.BoolNull(),
			DestinationName: types.StringNull(), Conditions: types.StringNull(),
		}}
		m.Edges = []PipelineEdgeModel{
			{From: types.StringValue("in"), To: types.StringValue("out")},
			{From: types.StringValue("out"), To: types.StringValue("dst")},
		}
		return m
	}

	if resp := validatePipeline(t, withNode("condition", v2Configured)); resp.Diagnostics.HasError() {
		t.Errorf("valid v2 node rejected: %v", resp.Diagnostics)
	}
	if resp := validatePipeline(t, withNode("condition", `[{"column":"a","operator":"=","value":"1"}]`)); resp.Diagnostics.HasError() {
		t.Errorf("valid v1 node rejected: %v", resp.Diagnostics)
	}

	for _, tc := range []struct{ name, nodeType, doc, summary string }{
		{"v1 document marked version 2", "condition",
			`{"version":2,"conditions":[{"column":"a","operator":"=","value":"1"}]}`, "Invalid conditions"},
		{"conditions on a stream node", "stream", v2Configured, "Conditions on a non-condition node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := validatePipeline(t, withNode(tc.nodeType, tc.doc))
			errs := resp.Diagnostics.Errors()
			if len(errs) != 1 || errs[0].Summary() != tc.summary {
				t.Fatalf("diagnostics = %v, want one %q error", errs, tc.summary)
			}
		})
	}
}

// TestPipelineModifyPlanRechecksConditions covers a v2 document that is
// unknown when the configuration is validated, as jsonencode() of another
// resource's output is, and turns out to carry a key the server drops. Plan
// time is the last chance for a clear error; after it, the apply would end in
// "Provider produced inconsistent result".
func TestPipelineModifyPlanRechecksConditions(t *testing.T) {
	ctx := context.Background()
	bad := v2Configured[:len(v2Configured)-len(`"version":2}`)]
	bad = strings.Replace(bad, `"operator":"Contains"`, `"ignoreCase":true,"operator":"Contains"`, 1) + `"version":2}`

	withConditions := func(c types.String) PipelineResourceModel {
		m := metricsPipeline()
		m.Nodes = append(m.Nodes[:1], PipelineNodeModel{
			ID: types.StringValue("filter"), Type: types.StringValue("condition"), IOType: types.StringNull(),
			PositionX: types.Float64Null(), PositionY: types.Float64Null(),
			StreamName: types.StringNull(), StreamType: types.StringNull(),
			FunctionName: types.StringNull(), AfterFlatten: types.BoolNull(),
			DestinationName: types.StringNull(), Conditions: c,
		}, m.Nodes[1])
		m.Edges = []PipelineEdgeModel{
			{From: types.StringValue("in"), To: types.StringValue("filter")},
			{From: types.StringValue("filter"), To: types.StringValue("out")},
		}
		return m
	}
	modifyPlan := func(model PipelineResourceModel) *resource.ModifyPlanResponse {
		t.Helper()
		config := pipelineConfig(t, model)
		plan := tfsdk.Plan(config)
		resp := &resource.ModifyPlanResponse{Plan: plan}
		(&PipelineResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Config: config, Plan: plan}, resp)
		return resp
	}

	// Unknown at validate time: nothing to check yet.
	if resp := validatePipeline(t, withConditions(types.StringUnknown())); resp.Diagnostics.HasError() {
		t.Fatalf("validate with unknown conditions: %v", resp.Diagnostics)
	}
	if resp := modifyPlan(withConditions(types.StringUnknown())); resp.Diagnostics.HasError() {
		t.Fatalf("plan with unknown conditions: %v", resp.Diagnostics)
	}

	// Known at plan time with a dropped key: a clear error on the attribute.
	resp := modifyPlan(withConditions(types.StringValue(bad)))
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || errs[0].Summary() != "Invalid conditions" || !strings.Contains(errs[0].Detail(), "`ignoreCase`") {
		t.Fatalf("diagnostics = %v, want one Invalid conditions error naming ignoreCase", errs)
	}

	// Known and valid: no error.
	if resp := modifyPlan(withConditions(types.StringValue(v2Configured))); resp.Diagnostics.HasError() {
		t.Fatalf("plan with valid conditions: %v", resp.Diagnostics)
	}

	// Destroy: no plan to check.
	config := pipelineConfig(t, withConditions(types.StringValue(bad)))
	nullPlan := tfsdk.Plan{Schema: config.Schema, Raw: tftypes.NewValue(config.Schema.Type().TerraformType(ctx), nil)}
	destroy := &resource.ModifyPlanResponse{Plan: nullPlan}
	(&PipelineResource{}).ModifyPlan(ctx, resource.ModifyPlanRequest{Plan: nullPlan}, destroy)
	if destroy.Diagnostics.HasError() {
		t.Fatalf("destroy plan: %v", destroy.Diagnostics)
	}
}
