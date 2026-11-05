package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// pipelineConfig turns a model into a tfsdk.Config for the pipeline schema.
func pipelineConfig(t *testing.T, model PipelineResourceModel) tfsdk.Config {
	t.Helper()
	ctx := context.Background()

	schemaResp := &resource.SchemaResponse{}
	(&PipelineResource{}).Schema(ctx, resource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema: %+v", schemaResp.Diagnostics)
	}

	plan := tfsdk.Plan{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	if diags := plan.Set(ctx, &model); diags.HasError() {
		t.Fatalf("building config: %+v", diags)
	}
	return tfsdk.Config{Schema: schemaResp.Schema, Raw: plan.Raw}
}

// metricsPipeline is the configuration from issue #14: a metrics source node
// with the pipeline-level stream_type left unset.
func metricsPipeline() PipelineResourceModel {
	return PipelineResourceModel{
		ID:          types.StringNull(),
		PipelineID:  types.StringNull(),
		OrgID:       types.StringNull(),
		Name:        types.StringValue("metrics-pipeline"),
		Description: types.StringNull(),
		Enabled:     types.BoolNull(),
		SourceType:  types.StringNull(),
		StreamName:  types.StringValue("cpu"),
		StreamType:  types.StringNull(),
		Version:     types.Int64Null(),
		Nodes: []PipelineNodeModel{
			{
				ID: types.StringValue("in"), Type: types.StringValue("stream"), IOType: types.StringValue("input"),
				PositionX: types.Float64Null(), PositionY: types.Float64Null(),
				StreamName: types.StringValue("cpu"), StreamType: types.StringValue("metrics"),
				FunctionName: types.StringNull(), AfterFlatten: types.BoolNull(),
				DestinationName: types.StringNull(), Conditions: types.StringNull(),
			},
			{
				ID: types.StringValue("out"), Type: types.StringValue("stream"), IOType: types.StringValue("output"),
				PositionX: types.Float64Null(), PositionY: types.Float64Null(),
				StreamName: types.StringValue("cpu_copy"), StreamType: types.StringNull(),
				FunctionName: types.StringNull(), AfterFlatten: types.BoolNull(),
				DestinationName: types.StringNull(), Conditions: types.StringNull(),
			},
		},
		Edges: []PipelineEdgeModel{{From: types.StringValue("in"), To: types.StringValue("out")}},
	}
}

func planStreamType(t *testing.T, model PipelineResourceModel) types.String {
	t.Helper()
	ctx := context.Background()
	config := pipelineConfig(t, model)

	req := planmodifier.StringRequest{
		Path:        path.Root("stream_type"),
		Config:      config,
		ConfigValue: model.StreamType,
		PlanValue:   types.StringUnknown(),
	}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	pipelineStreamTypeFromSourceNode{}.PlanModifyString(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("plan modifier: %+v", resp.Diagnostics)
	}
	return resp.PlanValue
}

func TestPipelineStreamTypeDefaultsToSourceNode(t *testing.T) {
	t.Parallel()

	if got := planStreamType(t, metricsPipeline()); got != types.StringValue("metrics") {
		t.Fatalf("stream_type planned as %v, want \"metrics\" from the source node", got)
	}
}

func TestPipelineStreamTypeFallsBackToLogs(t *testing.T) {
	t.Parallel()

	model := metricsPipeline()
	model.Nodes[0].StreamType = types.StringNull()
	if got := planStreamType(t, model); got != types.StringValue("logs") {
		t.Fatalf("stream_type planned as %v, want \"logs\"", got)
	}
}

func TestPipelineStreamTypeKeepsConfiguredValue(t *testing.T) {
	t.Parallel()

	model := metricsPipeline()
	model.StreamType = types.StringValue("metrics")
	if got := planStreamType(t, model); got != types.StringUnknown() {
		t.Fatalf("modifier touched a configured value: %v", got)
	}
}

func TestPipelineSourceStreamType(t *testing.T) {
	t.Parallel()

	node := func(typ, streamType types.String) PipelineNodeModel {
		return PipelineNodeModel{Type: typ, StreamType: streamType}
	}
	cases := []struct {
		name  string
		nodes []PipelineNodeModel
		want  types.String
	}{
		{"no nodes", nil, types.StringValue("logs")},
		{"traces source", []PipelineNodeModel{node(types.StringValue("stream"), types.StringValue("traces"))}, types.StringValue("traces")},
		{"source without type", []PipelineNodeModel{node(types.StringValue("stream"), types.StringNull())}, types.StringValue("logs")},
		{"unknown source type", []PipelineNodeModel{node(types.StringValue("stream"), types.StringUnknown())}, types.StringUnknown()},
		{"unknown node kind", []PipelineNodeModel{node(types.StringUnknown(), types.StringValue("metrics"))}, types.StringUnknown()},
		{"first node not a stream", []PipelineNodeModel{node(types.StringValue("function"), types.StringNull())}, types.StringValue("logs")},
	}
	for _, tc := range cases {
		if got := pipelineSourceStreamType(tc.nodes); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func validatePipeline(t *testing.T, model PipelineResourceModel) *resource.ValidateConfigResponse {
	t.Helper()
	resp := &resource.ValidateConfigResponse{}
	(&PipelineResource{}).ValidateConfig(context.Background(),
		resource.ValidateConfigRequest{Config: pipelineConfig(t, model)}, resp)
	return resp
}

func TestPipelineValidateConfigAcceptsMatchingSource(t *testing.T) {
	t.Parallel()

	model := metricsPipeline()
	if resp := validatePipeline(t, model); resp.Diagnostics.HasError() {
		t.Fatalf("unset stream_type rejected: %+v", resp.Diagnostics)
	}
	model.StreamType = types.StringValue("metrics")
	if resp := validatePipeline(t, model); resp.Diagnostics.HasError() {
		t.Fatalf("matching stream_type rejected: %+v", resp.Diagnostics)
	}
}

func TestPipelineValidateConfigRejectsMismatchedSource(t *testing.T) {
	t.Parallel()

	model := metricsPipeline()
	model.StreamType = types.StringValue("logs")
	resp := validatePipeline(t, model)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "stream_type") {
		t.Fatalf("stream_type mismatch not reported: %+v", resp.Diagnostics)
	}

	model = metricsPipeline()
	model.StreamName = types.StringValue("memory")
	resp = validatePipeline(t, model)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "stream_name") {
		t.Fatalf("stream_name mismatch not reported: %+v", resp.Diagnostics)
	}
}
