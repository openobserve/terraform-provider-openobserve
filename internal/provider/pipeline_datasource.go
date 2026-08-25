package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &FunctionDataSource{}
	_ datasource.DataSourceWithConfigure = &FunctionsDataSource{}
	_ datasource.DataSourceWithConfigure = &PipelinesDataSource{}
)

// ---------------------------------------------------------------------------
// Function
// ---------------------------------------------------------------------------

// NewFunctionDataSource returns a factory for the openobserve_function data source.
func NewFunctionDataSource() datasource.DataSource { return &FunctionDataSource{} }

// FunctionDataSource reads one transform function by name.
type FunctionDataSource struct{ client *Client }

// FunctionDataSourceModel holds the state for a function lookup.
type FunctionDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Name        types.String `tfsdk:"name"`
	Function    types.String `tfsdk:"function"`
	Params      types.String `tfsdk:"params"`
	Language    types.String `tfsdk:"language"`
	NumArgs     types.Int64  `tfsdk:"num_args"`
	UsedBy      types.List   `tfsdk:"used_by"`
	UsedByCount types.Int64  `tfsdk:"used_by_count"`
}

func (d *FunctionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_function"
}

func (d *FunctionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing transform function, including which pipelines use it.\n\n" +
			"`used_by` is what answers \"why can I not delete this function\": the server refuses to remove one " +
			"that a pipeline still references.",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{name}`."},
			"org_id":   schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"name":     schema.StringAttribute{Required: true, Description: "Function name to look up."},
			"function": schema.StringAttribute{Computed: true, Description: "The stored function body, including the trailing return expression the server appends to VRL."},
			"params":   schema.StringAttribute{Computed: true, Description: "Comma-separated parameter names."},
			"language": schema.StringAttribute{Computed: true, Description: "`vrl` or `js`."},
			"num_args": schema.Int64Attribute{Computed: true, Description: "Number of parameters."},
			"used_by": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Names of the pipelines that use this function.",
			},
			"used_by_count": schema.Int64Attribute{
				Computed:    true,
				Description: "How many pipelines use it. Non-zero means a delete will be refused.",
			},
		},
	}
}

func (d *FunctionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *FunctionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data FunctionDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()

	fn, err := d.client.GetFunction(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading function", err.Error())
		return
	}
	if fn == nil {
		resp.Diagnostics.AddError("Function not found", fmt.Sprintf("No function named %q in org %q.", name, org))
		return
	}

	deps, err := d.client.ListFunctionDependencies(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading function dependencies", err.Error())
		return
	}
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Name)
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(functionResourceID(org, name))
	data.Function = types.StringValue(fn.Function)
	data.Params = types.StringValue(fn.Params)
	data.Language = types.StringValue(functionLanguage(fn.TransType))
	data.NumArgs = types.Int64Value(fn.NumArgs)
	data.UsedBy = listFromStrings(ctx, names, &resp.Diagnostics)
	data.UsedByCount = types.Int64Value(int64(len(names)))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Functions
// ---------------------------------------------------------------------------

// NewFunctionsDataSource returns a factory for the openobserve_functions data source.
func NewFunctionsDataSource() datasource.DataSource { return &FunctionsDataSource{} }

// FunctionsDataSource lists every transform function.
type FunctionsDataSource struct{ client *Client }

// FunctionsDataSourceModel holds the state for a function listing.
type FunctionsDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	OrgID     types.String `tfsdk:"org_id"`
	Functions types.List   `tfsdk:"functions"`
}

var functionSummaryAttrTypes = map[string]attr.Type{
	"name":     types.StringType,
	"params":   types.StringType,
	"language": types.StringType,
	"num_args": types.Int64Type,
}

func (d *FunctionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_functions"
}

func (d *FunctionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the transform functions in an organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"functions": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every function in the organization.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     schema.StringAttribute{Computed: true, Description: "Function name."},
						"params":   schema.StringAttribute{Computed: true, Description: "Comma-separated parameter names."},
						"language": schema.StringAttribute{Computed: true, Description: "`vrl` or `js`."},
						"num_args": schema.Int64Attribute{Computed: true, Description: "Number of parameters."},
					},
				},
			},
		},
	}
}

func (d *FunctionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *FunctionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data FunctionsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	functions, err := d.client.ListFunctions(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing functions", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(functions))
	for _, fn := range functions {
		values = append(values, objectValue(functionSummaryAttrTypes, map[string]attr.Value{
			"name":     types.StringValue(fn.Name),
			"params":   types.StringValue(fn.Params),
			"language": types.StringValue(functionLanguage(fn.TransType)),
			"num_args": types.Int64Value(fn.NumArgs),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: functionSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(org)
	data.Functions = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Pipelines
// ---------------------------------------------------------------------------

// NewPipelinesDataSource returns a factory for the openobserve_pipelines data source.
func NewPipelinesDataSource() datasource.DataSource { return &PipelinesDataSource{} }

// PipelinesDataSource lists every pipeline.
type PipelinesDataSource struct{ client *Client }

// PipelinesDataSourceModel holds the state for a pipeline listing.
type PipelinesDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	OrgID     types.String `tfsdk:"org_id"`
	Pipelines types.List   `tfsdk:"pipelines"`
}

var pipelineSummaryAttrTypes = map[string]attr.Type{
	"pipeline_id": types.StringType,
	"name":        types.StringType,
	"description": types.StringType,
	"enabled":     types.BoolType,
	"source_type": types.StringType,
	"stream_name": types.StringType,
	"stream_type": types.StringType,
	"node_count":  types.Int64Type,
}

func (d *PipelinesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipelines"
}

func (d *PipelinesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the pipelines in an organization.\n\n" +
			"This is the fastest way to find the `pipeline_id` a `terraform import` needs, since a pipeline's " +
			"identifier is server-assigned and not visible in configuration.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"pipelines": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every pipeline in the organization.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"pipeline_id": schema.StringAttribute{Computed: true, Description: "Server-assigned identifier, which is what `terraform import` takes."},
						"name":        schema.StringAttribute{Computed: true, Description: "Pipeline name, as the server stored it."},
						"description": schema.StringAttribute{Computed: true, Description: "What the pipeline does."},
						"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether it is processing records."},
						"source_type": schema.StringAttribute{Computed: true, Description: "`realtime` or `scheduled`."},
						"stream_name": schema.StringAttribute{Computed: true, Description: "Source stream, for a realtime pipeline."},
						"stream_type": schema.StringAttribute{Computed: true, Description: "Type of the source stream."},
						"node_count":  schema.Int64Attribute{Computed: true, Description: "How many nodes the graph has."},
					},
				},
			},
		},
	}
}

func (d *PipelinesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *PipelinesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PipelinesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	pipelines, err := d.client.ListPipelines(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing pipelines", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(pipelines))
	for _, p := range pipelines {
		values = append(values, objectValue(pipelineSummaryAttrTypes, map[string]attr.Value{
			"pipeline_id": types.StringValue(p.ID),
			"name":        types.StringValue(p.Name),
			"description": types.StringValue(p.Description),
			"enabled":     types.BoolValue(p.Enabled),
			"source_type": types.StringValue(p.Source.SourceType),
			"stream_name": types.StringValue(p.Source.StreamName),
			"stream_type": types.StringValue(p.Source.StreamType),
			"node_count":  types.Int64Value(int64(len(p.Nodes))),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: pipelineSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(org)
	data.Pipelines = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
