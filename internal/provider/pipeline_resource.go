package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &PipelineResource{}
	_ resource.ResourceWithConfigure      = &PipelineResource{}
	_ resource.ResourceWithImportState    = &PipelineResource{}
	_ resource.ResourceWithValidateConfig = &PipelineResource{}
)

// pipelineNodeTypes are the node kinds the provider models.
var pipelineNodeTypes = []string{"stream", "function", "condition", "remote_stream"}

// NewPipelineResource returns a factory for the openobserve_pipeline resource.
func NewPipelineResource() resource.Resource { return &PipelineResource{} }

// PipelineResource manages a pipeline: a graph that transforms records between
// a source and one or more destinations.
type PipelineResource struct{ client *Client }

// PipelineResourceModel holds the Terraform state for a pipeline.
type PipelineResourceModel struct {
	ID          types.String        `tfsdk:"id"`
	PipelineID  types.String        `tfsdk:"pipeline_id"`
	OrgID       types.String        `tfsdk:"org_id"`
	Name        types.String        `tfsdk:"name"`
	Description types.String        `tfsdk:"description"`
	Enabled     types.Bool          `tfsdk:"enabled"`
	SourceType  types.String        `tfsdk:"source_type"`
	StreamName  types.String        `tfsdk:"stream_name"`
	StreamType  types.String        `tfsdk:"stream_type"`
	Nodes       []PipelineNodeModel `tfsdk:"node"`
	Edges       []PipelineEdgeModel `tfsdk:"edge"`
	Version     types.Int64         `tfsdk:"version"`
}

// PipelineNodeModel is one node block.
//
// The node payload is flat with a `type` discriminator rather than one nested
// block per kind. That is deliberate: Terraform materializes a single nested
// block driven by an empty `dynamic` as a present object full of unknowns
// rather than as absent, which makes "which variant did the author write"
// undecidable. A flat block sidesteps that entirely and keeps `dynamic "node"`
// working for callers who generate their graph.
type PipelineNodeModel struct {
	ID              types.String  `tfsdk:"id"`
	Type            types.String  `tfsdk:"type"`
	IOType          types.String  `tfsdk:"io_type"`
	PositionX       types.Float64 `tfsdk:"position_x"`
	PositionY       types.Float64 `tfsdk:"position_y"`
	StreamName      types.String  `tfsdk:"stream_name"`
	StreamType      types.String  `tfsdk:"stream_type"`
	FunctionName    types.String  `tfsdk:"function_name"`
	AfterFlatten    types.Bool    `tfsdk:"after_flatten"`
	DestinationName types.String  `tfsdk:"destination_name"`
	Conditions      types.String  `tfsdk:"conditions"`
}

// PipelineEdgeModel connects two nodes by id.
type PipelineEdgeModel struct {
	From types.String `tfsdk:"from"`
	To   types.String `tfsdk:"to"`
}

func (r *PipelineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline"
}

func (r *PipelineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve pipeline.\n\n" +
			"A pipeline is a graph. Records enter from a source stream, flow along `edge`s through `node`s that " +
			"transform or filter them, and land in an output: another stream, or an external endpoint through a " +
			"pipeline destination.\n\n" +
			"Reference `openobserve_function.<name>.name` from a function node and " +
			"`openobserve_pipeline_destination.<name>.name` from a remote_stream node. Those references are what " +
			"tell Terraform to create the function and destination first, and to destroy the pipeline before " +
			"them: the server refuses to delete either while a pipeline still uses it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{pipeline_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"pipeline_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned pipeline identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the pipeline belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Pipeline name. The server trims it and lowercases it, so `My Pipeline` is stored as " +
					"`my pipeline`. Writing it lowercase avoids the surprise.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What the pipeline does.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the pipeline processes records. A disabled pipeline stays configured but idle.",
			},
			"source_type": schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("realtime"),
				Validators: []validator.String{stringvalidator.OneOf("realtime")},
				Description: "How the pipeline is triggered. Only `realtime` is supported by this provider today: " +
					"records are processed as they are ingested into `stream_name`.",
			},
			"stream_name": schema.StringAttribute{
				Required: true,
				Description: "Source stream the pipeline consumes.\n\n" +
					"A stream can be the source of only one realtime pipeline. A second one is rejected with " +
					"*A realtime pipeline with same source stream already exists*.",
			},
			"stream_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("logs"),
				Validators:  []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
				Description: "Type of the source stream.",
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "Server-maintained revision, bumped on each edit.",
			},
		},
		Blocks: map[string]schema.Block{
			"node": schema.ListNestedBlock{
				Description: "One node of the graph. At least two nodes are required, and every node needs to be " +
					"reachable from the input node through `edge` blocks.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Required:    true,
							Description: "Node identifier, unique within the pipeline. `edge` blocks join nodes by this.",
						},
						"type": schema.StringAttribute{
							Required:   true,
							Validators: []validator.String{stringvalidator.OneOf(pipelineNodeTypes...)},
							Description: "What the node does:\n\n" +
								"- `stream`: reads from or writes to a stream. Needs `stream_name`.\n" +
								"- `function`: applies a transform. Needs `function_name`.\n" +
								"- `condition`: filters records. Needs `conditions`.\n" +
								"- `remote_stream`: forwards to a pipeline destination. Needs `destination_name`.",
						},
						"io_type": schema.StringAttribute{
							Optional:   true,
							Computed:   true,
							Validators: []validator.String{stringvalidator.OneOf("input", "output", "default")},
							Description: "The node's role: `input` for the source, `output` for a terminal node, " +
								"`default` for anything in between. Inferred when omitted, from the node's type " +
								"and whether any edge leaves it.",
						},
						"position_x": schema.Float64Attribute{
							Optional: true,
							Computed: true,
							Description: "Horizontal position in the visual editor. Purely cosmetic, and laid out " +
								"left to right in declaration order when omitted.",
						},
						"position_y": schema.Float64Attribute{
							Optional:    true,
							Computed:    true,
							Description: "Vertical position in the visual editor. Cosmetic; defaults to 0.",
						},
						"stream_name": schema.StringAttribute{
							Optional:    true,
							Description: "For a `stream` node, the stream read from or written to.",
						},
						"stream_type": schema.StringAttribute{
							Optional:    true,
							Computed:    true,
							Validators:  []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
							Description: "For a `stream` node, the type of that stream. Defaults to the pipeline's `stream_type`.",
						},
						"function_name": schema.StringAttribute{
							Optional: true,
							Description: "For a `function` node, the transform to apply. Reference " +
								"`openobserve_function.<name>.name` so Terraform creates it first.",
						},
						"after_flatten": schema.BoolAttribute{
							Optional: true,
							Computed: true,
							// Deliberately no default. A default would make the
							// planned value true on every node, including the
							// ones where the field means nothing, and the read
							// back would then disagree with the plan.
							Description: "For a `function` node, whether the transform runs after the record is " +
								"flattened. Defaults to true, and is meaningless on any other node type.",
						},
						"destination_name": schema.StringAttribute{
							Optional: true,
							Description: "For a `remote_stream` node, the destination to forward to. Reference " +
								"`openobserve_pipeline_destination.<name>.name` so Terraform orders them.",
						},
						"conditions": schema.StringAttribute{
							Optional: true,
							Description: "For a `condition` node, the filter as a JSON document. Build it with " +
								"`jsonencode()`. An empty condition set is rejected by the server.",
						},
					},
				},
			},
			"edge": schema.ListNestedBlock{
				Description: "A directed connection between two nodes. Records flow from `from` to `to`.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"from": schema.StringAttribute{Required: true, Description: "Source node id."},
						"to":   schema.StringAttribute{Required: true, Description: "Target node id."},
					},
				},
			},
		},
	}
}

// ValidateConfig checks the graph rules the provider can settle without the
// server: node ids are unique, edges name declared nodes, and each node type
// carries the field it needs.
func (r *PipelineResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config PipelineResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	ids := make(map[string]struct{}, len(config.Nodes))
	for i, node := range config.Nodes {
		nodePath := path.Root("node").AtListIndex(i)

		if knownAndSet(node.ID) {
			if _, dup := ids[node.ID.ValueString()]; dup {
				resp.Diagnostics.AddAttributeError(nodePath.AtName("id"), "Duplicate node id",
					fmt.Sprintf("Node id %q is used more than once. Edges join nodes by id, so ids must be unique.",
						node.ID.ValueString()))
			}
			ids[node.ID.ValueString()] = struct{}{}
		}

		// Only complain about a missing field once the type is actually known.
		if !knownAndSet(node.Type) {
			continue
		}
		switch node.Type.ValueString() {
		case "stream":
			requirePipelineAttr(resp, nodePath, node.StreamName, "stream_name", "stream")
		case "function":
			requirePipelineAttr(resp, nodePath, node.FunctionName, "function_name", "function")
		case "remote_stream":
			requirePipelineAttr(resp, nodePath, node.DestinationName, "destination_name", "remote_stream")
		case "condition":
			requirePipelineAttr(resp, nodePath, node.Conditions, "conditions", "condition")
		}
	}

	if len(config.Nodes) > 0 && len(config.Nodes) < 2 {
		resp.Diagnostics.AddError(
			"A pipeline needs at least two nodes",
			"Records have to come from somewhere and go somewhere, so a pipeline needs a source node and at "+
				"least one more, joined by an edge.",
		)
	}
	if len(config.Nodes) >= 2 && len(config.Edges) == 0 {
		resp.Diagnostics.AddError(
			"A pipeline needs at least one edge",
			"Nodes with no edges between them are not a graph. Add `edge` blocks joining the nodes by id.",
		)
	}

	// Every edge endpoint has to name a declared node. Only check this once all
	// the ids are known, since an unknown id cannot be matched.
	if len(ids) == len(config.Nodes) {
		for i, edge := range config.Edges {
			edgePath := path.Root("edge").AtListIndex(i)
			for _, end := range []struct {
				value types.String
				name  string
			}{{edge.From, "from"}, {edge.To, "to"}} {
				if !knownAndSet(end.value) {
					continue
				}
				if _, ok := ids[end.value.ValueString()]; !ok {
					resp.Diagnostics.AddAttributeError(edgePath.AtName(end.name), "Edge names an unknown node",
						fmt.Sprintf("No node block declares id %q. Edge endpoints are node ids, not names.",
							end.value.ValueString()))
				}
			}
		}
	}
}

func requirePipelineAttr(resp *resource.ValidateConfigResponse, nodePath path.Path, v types.String, attr, nodeType string) {
	if v.IsUnknown() || !v.IsNull() {
		return
	}
	resp.Diagnostics.AddAttributeError(
		nodePath.AtName(attr),
		"Missing "+attr,
		"`"+attr+"` is required on a `"+nodeType+"` node.",
	)
}

func (r *PipelineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// apiFromModel renders the graph, filling in what the server requires but a
// user should not have to think about: node positions, io_type, and edge ids.
func (r *PipelineResource) apiFromModel(model *PipelineResourceModel, org string) PipelineAPI {
	// A node is terminal when nothing leaves it, which is what makes it an
	// output rather than a transform.
	hasOutgoing := make(map[string]bool, len(model.Edges))
	hasIncoming := make(map[string]bool, len(model.Edges))
	for _, e := range model.Edges {
		hasOutgoing[e.From.ValueString()] = true
		hasIncoming[e.To.ValueString()] = true
	}

	nodes := make([]PipelineNodeAPI, 0, len(model.Nodes))
	for i, n := range model.Nodes {
		id := n.ID.ValueString()

		ioType := n.IOType.ValueString()
		if ioType == "" {
			switch {
			case !hasIncoming[id]:
				ioType = "input"
			case !hasOutgoing[id]:
				ioType = "output"
			default:
				ioType = "default"
			}
		}

		// Positions are cosmetic but required. Lay the graph out left to right
		// so a pipeline written in Terraform is still readable in the UI.
		x, y := float64(i)*250, 0.0
		if knownAndSet(n.PositionX) {
			x = n.PositionX.ValueFloat64()
		}
		if knownAndSet(n.PositionY) {
			y = n.PositionY.ValueFloat64()
		}

		streamType := n.StreamType.ValueString()
		if streamType == "" {
			streamType = model.StreamType.ValueString()
		}

		data := PipelineNodeDataAPI{NodeType: n.Type.ValueString()}
		switch n.Type.ValueString() {
		case "stream":
			data.OrgID = org
			data.StreamName = n.StreamName.ValueString()
			data.StreamType = streamType
		case "remote_stream":
			data.OrgID = org
			data.DestinationName = n.DestinationName.ValueString()
		case "function":
			data.Name = n.FunctionName.ValueString()
			// Unset means true: running after flatten is what a transform
			// almost always wants, and the field has no default in the schema
			// so that it stays null on nodes it does not apply to.
			afterFlatten := true
			if knownAndSet(n.AfterFlatten) {
				afterFlatten = n.AfterFlatten.ValueBool()
			}
			data.AfterFlatten = &afterFlatten
		case "condition":
			if knownAndSet(n.Conditions) {
				data.Conditions = []byte(n.Conditions.ValueString())
			}
		}

		nodes = append(nodes, PipelineNodeAPI{
			ID:       id,
			Data:     data,
			Position: PipelinePositionAPI{X: x, Y: y},
			IOType:   ioType,
		})
	}

	edges := make([]PipelineEdgeAPI, 0, len(model.Edges))
	for _, e := range model.Edges {
		from, to := e.From.ValueString(), e.To.ValueString()
		// The server's own convention for an edge id, which keeps a pipeline
		// written here looking like one built in the UI.
		edges = append(edges, PipelineEdgeAPI{ID: "e" + from + "-" + to, Source: from, Target: to})
	}

	return PipelineAPI{
		Name:        model.Name.ValueString(),
		Description: model.Description.ValueString(),
		Enabled:     model.Enabled.ValueBool(),
		Org:         org,
		Source: PipelineSourceAPI{
			SourceType: model.SourceType.ValueString(),
			OrgID:      org,
			StreamName: model.StreamName.ValueString(),
			StreamType: model.StreamType.ValueString(),
		},
		Nodes: nodes,
		Edges: edges,
	}
}

func (r *PipelineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.CreatePipeline(ctx, org, r.apiFromModel(&plan, org)); err != nil {
		resp.Diagnostics.AddError("Error creating pipeline", pipelineErrorDetail(err))
		return
	}

	// The create response carries an id, but the pipeline is looked up by name
	// so the same path serves an adopted pipeline.
	found, err := r.client.FindPipelineByName(ctx, org, plan.Name.ValueString())
	if err != nil || found == nil {
		resp.Diagnostics.AddError("Error resolving pipeline after create",
			"The pipeline was created but could not be found by name afterwards.")
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.PipelineID = types.StringValue(found.ID)
	plan.ID = types.StringValue(functionResourceID(org, found.ID))
	r.applyToModel(ctx, found, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	pipeline, err := r.client.GetPipeline(ctx, org, state.PipelineID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading pipeline", err.Error())
		return
	}
	if pipeline == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(functionResourceID(org, pipeline.ID))
	r.applyToModel(ctx, pipeline, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PipelineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PipelineResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	pipelineID := state.PipelineID.ValueString()

	body := r.apiFromModel(&plan, org)
	body.ID = pipelineID
	if err := r.client.UpdatePipeline(ctx, org, body); err != nil {
		resp.Diagnostics.AddError("Error updating pipeline", pipelineErrorDetail(err))
		return
	}

	// Pausing and resuming is its own endpoint rather than a field on the
	// update body, so a change to `enabled` is a separate call.
	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if err := r.client.SetPipelineEnabled(ctx, org, pipelineID, plan.Enabled.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error changing pipeline enabled state", pipelineErrorDetail(err))
			return
		}
	}

	pipeline, err := r.client.GetPipeline(ctx, org, pipelineID)
	if err != nil || pipeline == nil {
		resp.Diagnostics.AddError("Error reading pipeline after update",
			"The pipeline was updated but could not be read back.")
		return
	}
	plan.OrgID = types.StringValue(org)
	plan.PipelineID = types.StringValue(pipelineID)
	plan.ID = types.StringValue(functionResourceID(org, pipelineID))
	r.applyToModel(ctx, pipeline, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PipelineResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeletePipeline(ctx, org, state.PipelineID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting pipeline", pipelineErrorDetail(err))
	}
}

// ImportState supports: terraform import openobserve_pipeline.example default/7497861055431835648
func (r *PipelineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{pipeline_id}`, for example `default/7497861055431835648`.",
		)
		return
	}
	pipeline, err := r.client.GetPipeline(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading pipeline during import", err.Error())
		return
	}
	if pipeline == nil {
		resp.Diagnostics.AddError("Pipeline not found",
			fmt.Sprintf("Pipeline %q not found in org %q.", parts[1], parts[0]))
		return
	}
	state := PipelineResourceModel{
		ID:         types.StringValue(req.ID),
		OrgID:      types.StringValue(parts[0]),
		PipelineID: types.StringValue(parts[1]),
	}
	r.applyToModel(ctx, pipeline, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PipelineResource) applyToModel(ctx context.Context, api *PipelineAPI, model *PipelineResourceModel, diags *diag.Diagnostics) {
	// Keep the configured spelling of any JSON a node carries. jsonencode()
	// sorts keys alphabetically while the server returns them in struct order,
	// so comparing the two as strings reports a change on a configuration that
	// did not change, and fails the apply outright with an inconsistent result.
	priorConditions := make(map[string]types.String, len(model.Nodes))
	for _, n := range model.Nodes {
		if knownAndSet(n.ID) {
			priorConditions[n.ID.ValueString()] = n.Conditions
		}
	}

	model.Name = types.StringValue(api.Name)
	model.Description = types.StringValue(api.Description)
	model.Enabled = types.BoolValue(api.Enabled)
	model.Version = types.Int64Value(api.Version)
	model.PipelineID = types.StringValue(api.ID)

	if api.Source.SourceType != "" {
		model.SourceType = types.StringValue(api.Source.SourceType)
	}
	if api.Source.StreamName != "" {
		model.StreamName = types.StringValue(api.Source.StreamName)
	}
	if api.Source.StreamType != "" {
		model.StreamType = types.StringValue(api.Source.StreamType)
	}

	nodes := make([]PipelineNodeModel, 0, len(api.Nodes))
	for _, n := range api.Nodes {
		node := PipelineNodeModel{
			ID:        types.StringValue(n.ID),
			Type:      types.StringValue(n.Data.NodeType),
			IOType:    types.StringValue(n.IOType),
			PositionX: types.Float64Value(n.Position.X),
			PositionY: types.Float64Value(n.Position.Y),

			// Only one of these applies per node type; the rest stay null so
			// they match a configuration that did not set them.
			StreamName:      types.StringNull(),
			StreamType:      types.StringNull(),
			FunctionName:    types.StringNull(),
			DestinationName: types.StringNull(),
			Conditions:      types.StringNull(),
			AfterFlatten:    types.BoolNull(),
		}
		switch n.Data.NodeType {
		case "stream":
			node.StreamName = types.StringValue(n.Data.StreamName)
			node.StreamType = types.StringValue(n.Data.StreamType)
		case "remote_stream":
			node.DestinationName = types.StringValue(n.Data.DestinationName)
		case "function":
			node.FunctionName = types.StringValue(n.Data.Name)
			node.AfterFlatten = boolFromPtr(n.Data.AfterFlatten)
		case "condition":
			if len(n.Data.Conditions) > 0 {
				node.Conditions = reconcileJSON(priorConditions[n.ID], n.Data.Conditions, diags)
			}
		}
		nodes = append(nodes, node)
	}
	model.Nodes = nodes

	edges := make([]PipelineEdgeModel, 0, len(api.Edges))
	for _, e := range api.Edges {
		edges = append(edges, PipelineEdgeModel{
			From: types.StringValue(e.Source),
			To:   types.StringValue(e.Target),
		})
	}
	model.Edges = edges
}
