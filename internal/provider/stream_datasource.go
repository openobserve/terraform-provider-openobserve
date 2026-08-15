package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &StreamDataSource{}
	_ datasource.DataSourceWithConfigure = &StreamDataSource{}
	_ datasource.DataSource              = &StreamsDataSource{}
	_ datasource.DataSourceWithConfigure = &StreamsDataSource{}
)

// NewStreamDataSource returns a factory for the openobserve_stream data source.
func NewStreamDataSource() datasource.DataSource {
	return &StreamDataSource{}
}

// NewStreamsDataSource returns a factory for the openobserve_streams data source.
func NewStreamsDataSource() datasource.DataSource {
	return &StreamsDataSource{}
}

// StreamDataSource reads a single stream.
type StreamDataSource struct {
	client *Client
}

// StreamsDataSource lists the streams of an organization.
type StreamsDataSource struct {
	client *Client
}

// StreamDataSourceModel holds the state for a single stream lookup.
type StreamDataSourceModel struct {
	ID                          types.String `tfsdk:"id"`
	OrgID                       types.String `tfsdk:"org_id"`
	Name                        types.String `tfsdk:"name"`
	StreamType                  types.String `tfsdk:"stream_type"`
	StorageType                 types.String `tfsdk:"storage_type"`
	DataRetention               types.Int64  `tfsdk:"data_retention"`
	MaxQueryRange               types.Int64  `tfsdk:"max_query_range"`
	FullTextSearchKeys          types.Set    `tfsdk:"full_text_search_keys"`
	IndexFields                 types.Set    `tfsdk:"index_fields"`
	BloomFilterFields           types.Set    `tfsdk:"bloom_filter_fields"`
	DefinedSchemaFields         types.Set    `tfsdk:"defined_schema_fields"`
	StoreOriginalData           types.Bool   `tfsdk:"store_original_data"`
	IndexOriginalData           types.Bool   `tfsdk:"index_original_data"`
	IndexAllValues              types.Bool   `tfsdk:"index_all_values"`
	EnableDistinctFields        types.Bool   `tfsdk:"enable_distinct_fields"`
	EnableLogPatternsExtraction types.Bool   `tfsdk:"enable_log_patterns_extraction"`
	PartitionKeys               types.List   `tfsdk:"partition_keys"`
	Schema                      types.List   `tfsdk:"schema"`
	TotalFields                 types.Int64  `tfsdk:"total_fields"`
	DocCount                    types.Int64  `tfsdk:"doc_count"`
	StorageSize                 types.Int64  `tfsdk:"storage_size"`
	CompressedSize              types.Int64  `tfsdk:"compressed_size"`
}

// StreamsDataSourceModel holds the state for a stream listing.
type StreamsDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	OrgID      types.String `tfsdk:"org_id"`
	StreamType types.String `tfsdk:"stream_type"`
	Streams    types.List   `tfsdk:"streams"`
}

var streamSummaryAttrTypes = map[string]attr.Type{
	"name":           types.StringType,
	"stream_type":    types.StringType,
	"storage_type":   types.StringType,
	"data_retention": types.Int64Type,
	"total_fields":   types.Int64Type,
	"doc_count":      types.Int64Type,
	"storage_size":   types.Int64Type,
}

func (d *StreamDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stream"
}

func (d *StreamDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing OpenObserve stream's schema, settings, and statistics.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Data source ID in the format `{org_id}/{stream_type}/{name}`.",
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization identifier. Defaults to the provider's `org_id`.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Stream name.",
			},
			"stream_type": schema.StringAttribute{
				Required:    true,
				Description: "Stream type: `logs`, `metrics`, `traces`, or `metadata`.",
				Validators:  []validator.String{stringvalidator.OneOf(streamTypes...)},
			},
			"storage_type":    schema.StringAttribute{Computed: true, Description: "Storage tier of the stream."},
			"data_retention":  schema.Int64Attribute{Computed: true, Description: "Retention period in days."},
			"max_query_range": schema.Int64Attribute{Computed: true, Description: "Maximum query time range in hours."},
			"full_text_search_keys": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields included in full-text search.",
			},
			"index_fields": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields with a secondary index.",
			},
			"bloom_filter_fields": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields with bloom filter acceleration.",
			},
			"defined_schema_fields": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields kept in the user-defined schema.",
			},
			"store_original_data":            schema.BoolAttribute{Computed: true, Description: "Whether the original document is retained."},
			"index_original_data":            schema.BoolAttribute{Computed: true, Description: "Whether the original document is indexed."},
			"index_all_values":               schema.BoolAttribute{Computed: true, Description: "Whether every field value is indexed."},
			"enable_distinct_fields":         schema.BoolAttribute{Computed: true, Description: "Whether distinct value tracking is on."},
			"enable_log_patterns_extraction": schema.BoolAttribute{Computed: true, Description: "Whether log pattern extraction is on."},
			"partition_keys": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Partition keys configured on the stream.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"field":        schema.StringAttribute{Computed: true, Description: "Partition key field."},
						"type":         schema.StringAttribute{Computed: true, Description: "Partitioning strategy."},
						"hash_buckets": schema.Int64Attribute{Computed: true, Description: "Bucket count for hash partitioning."},
						"disabled":     schema.BoolAttribute{Computed: true, Description: "Whether the key has been retired."},
					},
				},
			},
			"schema": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Columns discovered in the stream.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Computed: true, Description: "Field name."},
						"type": schema.StringAttribute{Computed: true, Description: "Field data type."},
					},
				},
			},
			"total_fields":    schema.Int64Attribute{Computed: true, Description: "Number of fields in the stream schema."},
			"doc_count":       schema.Int64Attribute{Computed: true, Description: "Number of documents ingested."},
			"storage_size":    schema.Int64Attribute{Computed: true, Description: "Uncompressed storage size in bytes."},
			"compressed_size": schema.Int64Attribute{Computed: true, Description: "Compressed storage size in bytes."},
		},
	}
}

func (d *StreamDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *StreamDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data StreamDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	streamType := data.StreamType.ValueString()
	name := data.Name.ValueString()

	stream, err := d.client.GetStream(ctx, org, streamType, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading stream", err.Error())
		return
	}
	if stream == nil {
		resp.Diagnostics.AddError("Stream not found", fmt.Sprintf("Stream %q of type %q not found in org %q.", name, streamType, org))
		return
	}

	s := stream.Settings
	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(streamID(org, streamType, name))
	data.StorageType = types.StringValue(firstNonEmpty(s.StorageType, stream.StorageType))
	data.DataRetention = types.Int64Value(s.DataRetention)
	data.MaxQueryRange = types.Int64Value(s.MaxQueryRange)
	data.FullTextSearchKeys = setFromStrings(ctx, s.FullTextSearchKeys, &resp.Diagnostics)
	data.IndexFields = setFromStrings(ctx, s.IndexFields, &resp.Diagnostics)
	data.BloomFilterFields = setFromStrings(ctx, s.BloomFilterFields, &resp.Diagnostics)
	data.DefinedSchemaFields = setFromStrings(ctx, s.DefinedSchemaFields, &resp.Diagnostics)
	data.StoreOriginalData = types.BoolValue(s.StoreOriginalData)
	data.IndexOriginalData = types.BoolValue(s.IndexOriginalData)
	data.IndexAllValues = types.BoolValue(s.IndexAllValues)
	data.EnableDistinctFields = types.BoolValue(s.EnableDistinctFields)
	data.EnableLogPatternsExtraction = types.BoolValue(s.EnableLogPatternsExtraction)
	data.PartitionKeys = partitionKeysToList(s.PartitionKeys, &resp.Diagnostics)
	data.Schema = streamFieldsToList(stream.Schema, &resp.Diagnostics)
	data.TotalFields = types.Int64Value(int64(stream.TotalFields))

	if stream.Stats != nil {
		data.DocCount = types.Int64Value(stream.Stats.DocNum)
		data.StorageSize = types.Int64Value(int64(stream.Stats.StorageSize))
		data.CompressedSize = types.Int64Value(int64(stream.Stats.CompressedSize))
	} else {
		data.DocCount = types.Int64Value(0)
		data.StorageSize = types.Int64Value(0)
		data.CompressedSize = types.Int64Value(0)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *StreamsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_streams"
}

func (d *StreamsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the streams in an OpenObserve organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization identifier. Defaults to the provider's `org_id`.",
			},
			"stream_type": schema.StringAttribute{
				Optional:    true,
				Description: "Restrict the listing to one stream type. All types are returned when omitted.",
				Validators:  []validator.String{stringvalidator.OneOf(streamTypes...)},
			},
			"streams": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The streams found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":           schema.StringAttribute{Computed: true, Description: "Stream name."},
						"stream_type":    schema.StringAttribute{Computed: true, Description: "Stream type."},
						"storage_type":   schema.StringAttribute{Computed: true, Description: "Storage tier."},
						"data_retention": schema.Int64Attribute{Computed: true, Description: "Retention period in days."},
						"total_fields":   schema.Int64Attribute{Computed: true, Description: "Number of fields in the schema."},
						"doc_count":      schema.Int64Attribute{Computed: true, Description: "Number of documents ingested."},
						"storage_size":   schema.Int64Attribute{Computed: true, Description: "Uncompressed storage size in bytes."},
					},
				},
			},
		},
	}
}

func (d *StreamsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *StreamsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data StreamsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	streams, err := d.client.ListStreams(ctx, org, data.StreamType.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error listing streams", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(streams))
	for _, s := range streams {
		var docCount, storageSize int64
		if s.Stats != nil {
			docCount = s.Stats.DocNum
			storageSize = int64(s.Stats.StorageSize)
		}
		values = append(values, objectValue(streamSummaryAttrTypes, map[string]attr.Value{
			"name":           types.StringValue(s.Name),
			"stream_type":    types.StringValue(s.StreamType),
			"storage_type":   types.StringValue(firstNonEmpty(s.Settings.StorageType, s.StorageType)),
			"data_retention": types.Int64Value(s.Settings.DataRetention),
			"total_fields":   types.Int64Value(int64(s.TotalFields)),
			"doc_count":      types.Int64Value(docCount),
			"storage_size":   types.Int64Value(storageSize),
		}, &resp.Diagnostics))
	}

	list, d2 := types.ListValue(types.ObjectType{AttrTypes: streamSummaryAttrTypes}, values)
	resp.Diagnostics.Append(d2...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/streams/%s", org, data.StreamType.ValueString()))
	data.Streams = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// partitionKeysToList renders partition keys as a Terraform list.
func partitionKeysToList(in []StreamPartitionAPI, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0, len(in))
	for _, pk := range in {
		kind, buckets := partitionTypeFromAPI(pk.Types)
		values = append(values, objectValue(streamPartitionKeyAttrTypes, map[string]attr.Value{
			"field":        types.StringValue(pk.Field),
			"type":         types.StringValue(kind),
			"hash_buckets": buckets,
			"disabled":     types.BoolValue(pk.Disabled),
		}, diags))
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: streamPartitionKeyAttrTypes}, values)
	diags.Append(d...)
	return list
}

// streamFieldsToList renders a stream schema as a Terraform list.
func streamFieldsToList(in []StreamFieldAPI, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0, len(in))
	for _, f := range in {
		values = append(values, objectValue(streamSchemaFieldAttrTypes, map[string]attr.Value{
			"name": types.StringValue(f.Name),
			"type": types.StringValue(f.Type),
		}, diags))
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: streamSchemaFieldAttrTypes}, values)
	diags.Append(d...)
	return list
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
