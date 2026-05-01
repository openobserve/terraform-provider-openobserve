package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &StreamDataSource{}

// NewStreamDataSource returns a factory for the openobserve_stream data source.
func NewStreamDataSource() datasource.DataSource {
	return &StreamDataSource{}
}

// StreamDataSource reads an existing OpenObserve stream.
type StreamDataSource struct {
	client *Client
}

// StreamDataSourceModel holds state for the data source.
type StreamDataSourceModel struct {
	ID          types.String         `tfsdk:"id"`
	OrgID       types.String         `tfsdk:"org_id"`
	Name        types.String         `tfsdk:"name"`
	StreamType  types.String         `tfsdk:"stream_type"`
	StorageType types.String         `tfsdk:"storage_type"`
	Settings    *StreamSettingsModel `tfsdk:"settings"`
}

func (d *StreamDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stream"
}

func (d *StreamDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads settings for an existing OpenObserve stream.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Data source ID in the format `{org_id}/{stream_type}/{name}`.",
			},
			"org_id": schema.StringAttribute{
				Required:    true,
				Description: "Organization identifier.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Stream name.",
			},
			"stream_type": schema.StringAttribute{
				Required:    true,
				Description: "Stream type: `logs`, `metrics`, or `traces`.",
				Validators: []validator.String{
					stringvalidator.OneOf("logs", "metrics", "traces"),
				},
			},
			"storage_type": schema.StringAttribute{
				Computed:    true,
				Description: "Storage backend reported by OpenObserve (e.g. `s3`, `local`).",
			},
			"settings": schema.SingleNestedAttribute{
				Computed:    true,
				Description: "Stream-level settings as configured in OpenObserve.",
				Attributes: map[string]schema.Attribute{
					"data_retention": schema.Int64Attribute{
						Computed:    true,
						Description: "Days data is retained.",
					},
					"full_text_search_keys": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields included in full-text search.",
					},
					"index_fields": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields that are indexed.",
					},
					"bloom_filter_fields": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
						Description: "Fields with bloom filter acceleration.",
					},
					"partition_keys": schema.ListNestedAttribute{
						Computed:    true,
						Description: "Partition keys.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"field": schema.StringAttribute{
									Computed: true,
								},
								"types": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *StreamDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *StreamDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data StreamDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	streamSchema, err := d.client.GetStreamSchema(ctx, data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading stream", err.Error())
		return
	}
	if streamSchema == nil {
		resp.Diagnostics.AddError(
			"Stream not found",
			fmt.Sprintf("Stream %q (type: %s) not found in org %q.", data.Name.ValueString(), data.StreamType.ValueString(), data.OrgID.ValueString()),
		)
		return
	}

	data.ID = types.StringValue(streamID(data.OrgID.ValueString(), data.StreamType.ValueString(), data.Name.ValueString()))
	data.StorageType = types.StringValue(streamSchema.StorageType)

	var pkObjects []attr.Value
	for _, pk := range streamSchema.Settings.PartitionKeys {
		pkTypes, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(pk.Types))
		obj, _ := types.ObjectValue(partitionKeyAttrTypes, map[string]attr.Value{
			"field": types.StringValue(pk.Field),
			"types": pkTypes,
		})
		pkObjects = append(pkObjects, obj)
	}
	pks, _ := types.ListValue(types.ObjectType{AttrTypes: partitionKeyAttrTypes}, orEmptyAttr(pkObjects))
	ftsk, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(streamSchema.Settings.FullTextSearchKeys))
	idf, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(streamSchema.Settings.IndexFields))
	bff, _ := types.ListValueFrom(ctx, types.StringType, orEmpty(streamSchema.Settings.BloomFilterFields))

	data.Settings = &StreamSettingsModel{
		DataRetention:      types.Int64Value(int64(streamSchema.Settings.DataRetention)),
		FullTextSearchKeys: ftsk,
		IndexFields:        idf,
		BloomFilterFields:  bff,
		PartitionKeys:      pks,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
