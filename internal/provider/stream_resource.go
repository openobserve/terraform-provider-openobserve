package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &StreamResource{}
var _ resource.ResourceWithImportState = &StreamResource{}

// NewStreamResource returns a factory for the openobserve_stream resource.
func NewStreamResource() resource.Resource {
	return &StreamResource{}
}

// StreamResource manages an OpenObserve stream's settings.
type StreamResource struct {
	client *Client
}

// StreamResourceModel holds the Terraform state for a stream.
type StreamResourceModel struct {
	ID          types.String         `tfsdk:"id"`
	OrgID       types.String         `tfsdk:"org_id"`
	Name        types.String         `tfsdk:"name"`
	StreamType  types.String         `tfsdk:"stream_type"`
	Settings    *StreamSettingsModel `tfsdk:"settings"`
}

// StreamSettingsModel holds the nested settings block.
type StreamSettingsModel struct {
	DataRetention      types.Int64 `tfsdk:"data_retention"`
	FullTextSearchKeys types.List  `tfsdk:"full_text_search_keys"`
	IndexFields        types.List  `tfsdk:"index_fields"`
	BloomFilterFields  types.List  `tfsdk:"bloom_filter_fields"`
	PartitionKeys      types.List  `tfsdk:"partition_keys"`
}

// PartitionKeyModel holds a single partition key entry.
type PartitionKeyModel struct {
	Field types.String `tfsdk:"field"`
	Types types.List   `tfsdk:"types"`
}

var partitionKeyAttrTypes = map[string]attr.Type{
	"field": types.StringType,
	"types": types.ListType{ElemType: types.StringType},
}

func (r *StreamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stream"
}

func (r *StreamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve stream's settings including data retention, partitioning, and indexing.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource ID in the format `{org_id}/{stream_type}/{name}`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"org_id": schema.StringAttribute{
				Required:    true,
				Description: "Organization identifier (e.g. `default`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Stream name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"stream_type": schema.StringAttribute{
				Required:    true,
				Description: "Stream type: `logs`, `metrics`, or `traces`.",
				Validators: []validator.String{
					stringvalidator.OneOf("logs", "metrics", "traces"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"settings": schema.SingleNestedAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Stream-level settings for data retention, indexing, and partitioning.",
				Attributes: map[string]schema.Attribute{
					"data_retention": schema.Int64Attribute{
						Optional:    true,
						Computed:    true,
						Description: "Number of days to retain data. Defaults to the cluster-wide retention setting.",
						Default:     int64default.StaticInt64(0),
					},
					"full_text_search_keys": schema.ListAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields included in full-text search.",
						Default:     listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
					},
					"index_fields": schema.ListAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields that are indexed for fast filtering.",
						Default:     listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
					},
					"bloom_filter_fields": schema.ListAttribute{
						Optional:    true,
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields with bloom filter acceleration.",
						Default:     listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
					},
					"partition_keys": schema.ListNestedAttribute{
						Optional:    true,
						Computed:    true,
						Description: "Partition keys for data distribution and query pruning.",
						Default:     listdefault.StaticValue(types.ListValueMust(types.ObjectType{AttrTypes: partitionKeyAttrTypes}, []attr.Value{})),
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"field": schema.StringAttribute{
									Required:    true,
									Description: "Log field used as the partition key.",
								},
								"types": schema.ListAttribute{
									Required:    true,
									ElementType: types.StringType,
									Description: "Partition key type(s). Typically `[\"value\"]`.",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *StreamResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *StreamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data StreamResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings, d := modelToStreamSettings(ctx, data.Settings)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateStreamSettings(ctx, data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString(), settings); err != nil {
		resp.Diagnostics.AddError("Error creating stream settings", err.Error())
		return
	}

	data.ID = types.StringValue(streamID(data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString()))

	// Re-read to populate computed fields from the API
	r.refreshState(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StreamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data StreamResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.refreshState(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StreamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data StreamResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings, d := modelToStreamSettings(ctx, data.Settings)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateStreamSettings(ctx, data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString(), settings); err != nil {
		resp.Diagnostics.AddError("Error updating stream settings", err.Error())
		return
	}

	r.refreshState(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StreamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data StreamResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteStream(ctx, data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting stream", err.Error())
	}
}

// ImportState supports: terraform import openobserve_stream.example default/logs/my-stream
func (r *StreamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be in the format {org_id}/{stream_type}/{name}, e.g. default/logs/my-stream",
		)
		return
	}

	data := StreamResourceModel{
		ID:         types.StringValue(req.ID),
		OrgID:      types.StringValue(parts[0]),
		StreamType: types.StringValue(parts[1]),
		Name:       types.StringValue(parts[2]),
	}

	r.refreshState(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func streamID(orgID, streamType, name string) string {
	return fmt.Sprintf("%s/%s/%s", orgID, streamType, name)
}

func (r *StreamResource) refreshState(ctx context.Context, data *StreamResourceModel, diags *diag.Diagnostics) {
	streamSchema, err := r.client.GetStreamSchema(ctx, data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString())
	if err != nil {
		diags.AddError("Error reading stream schema", err.Error())
		return
	}
	if streamSchema == nil {
		data.ID = types.StringNull()
		return
	}

	settings, d := streamSettingsToModel(ctx, streamSchema.Settings)
	if d != nil {
		diags.AddError("Error mapping stream settings", d.Error())
		return
	}
	data.Settings = settings
}

// modelToStreamSettings converts the Terraform model to the API wire type.
func modelToStreamSettings(ctx context.Context, model *StreamSettingsModel) (StreamSettingsAPI, diag.Diagnostics) {
	if model == nil {
		return StreamSettingsAPI{}, nil
	}

	var ftsk, idf, bff []string
	_ = model.FullTextSearchKeys.ElementsAs(ctx, &ftsk, false)
	_ = model.IndexFields.ElementsAs(ctx, &idf, false)
	_ = model.BloomFilterFields.ElementsAs(ctx, &bff, false)

	var pkModels []PartitionKeyModel
	_ = model.PartitionKeys.ElementsAs(ctx, &pkModels, false)

	var pks []StreamPartitionKey
	for _, pk := range pkModels {
		var pkTypes []string
		_ = pk.Types.ElementsAs(ctx, &pkTypes, false)
		pks = append(pks, StreamPartitionKey{Field: pk.Field.ValueString(), Types: pkTypes})
	}

	return StreamSettingsAPI{
		DataRetention:      int(model.DataRetention.ValueInt64()),
		FullTextSearchKeys: ftsk,
		IndexFields:        idf,
		BloomFilterFields:  bff,
		PartitionKeys:      pks,
	}, nil
}

// streamSettingsToModel converts the API wire type to the Terraform model.
func streamSettingsToModel(ctx context.Context, api StreamSettingsAPI) (*StreamSettingsModel, error) {
	ftsk, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(api.FullTextSearchKeys))
	idf, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(api.IndexFields))
	bff, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(api.BloomFilterFields))

	var pkObjects []attr.Value
	for _, pk := range api.PartitionKeys {
		pkTypes, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(pk.Types))
		obj, _ := types.ObjectValue(partitionKeyAttrTypes, map[string]attr.Value{
			"field": types.StringValue(pk.Field),
			"types": pkTypes,
		})
		pkObjects = append(pkObjects, obj)
	}
	pks, _ := types.ListValue(types.ObjectType{AttrTypes: partitionKeyAttrTypes}, orEmptyAttr(pkObjects))

	return &StreamSettingsModel{
		DataRetention:      types.Int64Value(int64(api.DataRetention)),
		FullTextSearchKeys: ftsk,
		IndexFields:        idf,
		BloomFilterFields:  bff,
		PartitionKeys:      pks,
	}, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyAttr(s []attr.Value) []attr.Value {
	if s == nil {
		return []attr.Value{}
	}
	return s
}
