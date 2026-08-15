package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &StreamResource{}
	_ resource.ResourceWithConfigure   = &StreamResource{}
	_ resource.ResourceWithImportState = &StreamResource{}
)

// streamTypes are the stream types a user may manage declaratively. Index and
// enrichment table streams are rejected by the settings endpoint.
var streamTypes = []string{"logs", "metrics", "traces", "metadata"}

// NewStreamResource returns a factory for the openobserve_stream resource.
func NewStreamResource() resource.Resource {
	return &StreamResource{}
}

// StreamResource manages an OpenObserve stream and its settings.
type StreamResource struct {
	client *Client
}

// StreamResourceModel holds the Terraform state for a stream.
type StreamResourceModel struct {
	ID                          types.String `tfsdk:"id"`
	OrgID                       types.String `tfsdk:"org_id"`
	Name                        types.String `tfsdk:"name"`
	StreamType                  types.String `tfsdk:"stream_type"`
	DataRetention               types.Int64  `tfsdk:"data_retention"`
	MaxQueryRange               types.Int64  `tfsdk:"max_query_range"`
	FlattenLevel                types.Int64  `tfsdk:"flatten_level"`
	StoreOriginalData           types.Bool   `tfsdk:"store_original_data"`
	ApproxPartition             types.Bool   `tfsdk:"approx_partition"`
	IndexOriginalData           types.Bool   `tfsdk:"index_original_data"`
	IndexAllValues              types.Bool   `tfsdk:"index_all_values"`
	EnableDistinctFields        types.Bool   `tfsdk:"enable_distinct_fields"`
	EnableLogPatternsExtraction types.Bool   `tfsdk:"enable_log_patterns_extraction"`
	FullTextSearchKeys          types.Set    `tfsdk:"full_text_search_keys"`
	IndexFields                 types.Set    `tfsdk:"index_fields"`
	BloomFilterFields           types.Set    `tfsdk:"bloom_filter_fields"`
	DefinedSchemaFields         types.Set    `tfsdk:"defined_schema_fields"`
	DistinctValueFields         types.Set    `tfsdk:"distinct_value_fields"`
	PartitionKeys               types.List   `tfsdk:"partition_keys"`
	StorageType                 types.String `tfsdk:"storage_type"`
	Schema                      types.List   `tfsdk:"schema"`
}

// StreamPartitionKeyModel is one entry of the partition_keys list.
type StreamPartitionKeyModel struct {
	Field       types.String `tfsdk:"field"`
	Type        types.String `tfsdk:"type"`
	HashBuckets types.Int64  `tfsdk:"hash_buckets"`
	Disabled    types.Bool   `tfsdk:"disabled"`
}

var streamPartitionKeyAttrTypes = map[string]attr.Type{
	"field":        types.StringType,
	"type":         types.StringType,
	"hash_buckets": types.Int64Type,
	"disabled":     types.BoolType,
}

var streamSchemaFieldAttrTypes = map[string]attr.Type{
	"name": types.StringType,
	"type": types.StringType,
}

func (r *StreamResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_stream"
}

func (r *StreamResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve stream and its settings: retention, partitioning, indexing, and schema options.\n\n" +
			"Applying this resource to a stream name that does not exist yet creates it. Applying it to a stream that " +
			"already exists (because data has been ingested into it) adopts the stream and manages its settings.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{stream_type}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization identifier. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Stream name.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"stream_type": schema.StringAttribute{
				Required:      true,
				Description:   "Stream type: `logs`, `metrics`, `traces`, or `metadata`.",
				Validators:    []validator.String{stringvalidator.OneOf(streamTypes...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"data_retention": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Retention period in days. `0` inherits the cluster-wide default.",
			},
			"max_query_range": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "Maximum query time range in hours. `0` means unlimited.",
			},
			"flatten_level": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Description: "How many levels deep nested JSON is flattened on ingestion.",
			},
			"store_original_data": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Keep the original unflattened document alongside the parsed one.",
			},
			"approx_partition": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Use approximate partitioning for faster ingestion.",
			},
			"index_original_data": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Include the original document in the full-text index.",
			},
			"index_all_values": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Index every field value rather than only the configured index fields.",
			},
			"enable_distinct_fields": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Track distinct values for the fields listed in `distinct_value_fields`.",
			},
			"enable_log_patterns_extraction": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Extract log patterns from ingested records.",
			},
			"full_text_search_keys": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields included in full-text search.",
			},
			"index_fields": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields with a secondary index for fast filtering.",
			},
			"bloom_filter_fields": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields with bloom filter acceleration.",
			},
			"defined_schema_fields": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields kept in the user-defined schema. Other fields move to the catch-all column.",
			},
			"distinct_value_fields": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Fields whose distinct values are tracked. Requires `enable_distinct_fields`.",
			},
			"partition_keys": schema.ListNestedAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Partition keys used for data layout and query pruning.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"field": schema.StringAttribute{
							Required:    true,
							Description: "Field used as the partition key.",
						},
						"type": schema.StringAttribute{
							Optional:    true,
							Computed:    true,
							Description: "Partitioning strategy: `value` (default), `prefix`, or `hash`.",
							Validators:  []validator.String{stringvalidator.OneOf("value", "prefix", "hash")},
						},
						"hash_buckets": schema.Int64Attribute{
							Optional:    true,
							Description: "Number of buckets when `type` is `hash`.",
						},
						"disabled": schema.BoolAttribute{
							Computed:    true,
							Description: "True when the partition key has been retired but is retained for old data.",
						},
					},
				},
			},
			"storage_type": schema.StringAttribute{
				Computed:    true,
				Description: "Storage tier reported by the server.",
			},
			"schema": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Columns discovered in the stream, as reported by the server.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Computed: true, Description: "Field name."},
						"type": schema.StringAttribute{Computed: true, Description: "Field data type."},
					},
				},
			},
		},
	}
}

func (r *StreamResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *StreamResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan StreamResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	streamType := plan.StreamType.ValueString()
	name := plan.Name.ValueString()

	// The stream may already exist because data was ingested into it. Adopting
	// it is the useful behaviour, so a create conflict is not an error.
	if err := r.client.CreateStream(ctx, org, streamType, name, CreateStreamAPI{}); err != nil && !isAlreadyExists(err) {
		resp.Diagnostics.AddError("Error creating stream", err.Error())
		return
	}

	if !r.applySettings(ctx, org, streamType, name, &plan, &resp.Diagnostics) {
		return
	}

	plan.ID = types.StringValue(streamID(org, streamType, name))
	plan.OrgID = types.StringValue(org)
	r.readInto(ctx, org, streamType, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *StreamResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state StreamResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	stream, err := r.client.GetStream(ctx, org, state.StreamType.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading stream", err.Error())
		return
	}
	if stream == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(streamID(org, state.StreamType.ValueString(), state.Name.ValueString()))
	r.applyStreamToModel(ctx, stream, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *StreamResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan StreamResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	streamType := plan.StreamType.ValueString()
	name := plan.Name.ValueString()

	if !r.applySettings(ctx, org, streamType, name, &plan, &resp.Diagnostics) {
		return
	}

	plan.ID = types.StringValue(streamID(org, streamType, name))
	plan.OrgID = types.StringValue(org)
	r.readInto(ctx, org, streamType, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *StreamResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state StreamResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteStream(ctx, org, state.StreamType.ValueString(), state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting stream", err.Error())
	}
}

// ImportState supports: terraform import openobserve_stream.example default/logs/my-stream
func (r *StreamResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{stream_type}/{name}`, for example `default/logs/my-stream`.",
		)
		return
	}

	state := StreamResourceModel{
		ID:         types.StringValue(req.ID),
		OrgID:      types.StringValue(parts[0]),
		StreamType: types.StringValue(parts[1]),
		Name:       types.StringValue(parts[2]),
	}

	stream, err := r.client.GetStream(ctx, parts[0], parts[1], parts[2])
	if err != nil {
		resp.Diagnostics.AddError("Error reading stream during import", err.Error())
		return
	}
	if stream == nil {
		resp.Diagnostics.AddError("Stream not found", fmt.Sprintf("Stream %q of type %q not found in org %q.", parts[2], parts[1], parts[0]))
		return
	}

	r.applyStreamToModel(ctx, stream, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------------------
// Settings reconciliation
// ---------------------------------------------------------------------------

// applySettings diffs the desired settings against the server's current
// settings and pushes the delta. It reports whether the call succeeded.
func (r *StreamResource) applySettings(ctx context.Context, org, streamType, name string, plan *StreamResourceModel, diags *diag.Diagnostics) bool {
	current, err := r.client.GetStream(ctx, org, streamType, name)
	if err != nil {
		diags.AddError("Error reading current stream settings", err.Error())
		return false
	}

	var cur StreamSettingsAPI
	if current != nil {
		cur = current.Settings
	}

	update := UpdateStreamSettingsAPI{
		DataRetention:               optInt64(plan.DataRetention),
		MaxQueryRange:               optInt64(plan.MaxQueryRange),
		FlattenLevel:                optInt64(plan.FlattenLevel),
		StoreOriginalData:           optBool(plan.StoreOriginalData),
		ApproxPartition:             optBool(plan.ApproxPartition),
		IndexOriginalData:           optBool(plan.IndexOriginalData),
		IndexAllValues:              optBool(plan.IndexAllValues),
		EnableDistinctFields:        optBool(plan.EnableDistinctFields),
		EnableLogPatternsExtraction: optBool(plan.EnableLogPatternsExtraction),
	}

	update.FullTextSearchKeys = newAddRemove(diffStrings(cur.FullTextSearchKeys, stringsFromSet(ctx, plan.FullTextSearchKeys, diags)))
	update.IndexFields = newAddRemove(diffStrings(cur.IndexFields, stringsFromSet(ctx, plan.IndexFields, diags)))
	update.BloomFilterFields = newAddRemove(diffStrings(cur.BloomFilterFields, stringsFromSet(ctx, plan.BloomFilterFields, diags)))
	update.DefinedSchemaFields = newAddRemove(diffStrings(cur.DefinedSchemaFields, stringsFromSet(ctx, plan.DefinedSchemaFields, diags)))

	currentDistinct := make([]string, 0, len(cur.DistinctValueFields))
	for _, d := range cur.DistinctValueFields {
		currentDistinct = append(currentDistinct, d.Name)
	}
	sort.Strings(currentDistinct)
	update.DistinctValueFields = newAddRemove(diffStrings(currentDistinct, stringsFromSet(ctx, plan.DistinctValueFields, diags)))

	addPK, removePK := r.diffPartitionKeys(ctx, cur.PartitionKeys, plan.PartitionKeys, diags)
	update.PartitionKeys = newAddRemove(addPK, removePK)

	if diags.HasError() {
		return false
	}

	if err := r.client.UpdateStreamSettings(ctx, org, streamType, name, update); err != nil {
		diags.AddError("Error updating stream settings", err.Error())
		return false
	}
	return true
}

// diffPartitionKeys computes the partition keys to add and remove. A key whose
// strategy changed is removed and re-added.
func (r *StreamResource) diffPartitionKeys(ctx context.Context, current []StreamPartitionAPI, planned types.List, diags *diag.Diagnostics) (add, remove []StreamPartitionAPI) {
	if planned.IsNull() || planned.IsUnknown() {
		return nil, nil
	}

	var models []StreamPartitionKeyModel
	diags.Append(planned.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, nil
	}

	desired := make(map[string]StreamPartitionAPI, len(models))
	order := make([]string, 0, len(models))
	for _, m := range models {
		pk := StreamPartitionAPI{
			Field: m.Field.ValueString(),
			Types: partitionTypeToAPI(m.Type.ValueString(), m.HashBuckets),
		}
		desired[pk.Field] = pk
		order = append(order, pk.Field)
	}

	currentByField := make(map[string]StreamPartitionAPI, len(current))
	for _, pk := range current {
		if pk.Disabled {
			// A disabled key is already retired; treat it as absent.
			continue
		}
		currentByField[pk.Field] = pk
	}

	for _, field := range order {
		want := desired[field]
		have, ok := currentByField[field]
		if !ok || !samePartitionType(have.Types, want.Types) {
			add = append(add, want)
		}
	}
	for field, have := range currentByField {
		if _, ok := desired[field]; !ok {
			remove = append(remove, have)
		}
	}
	sort.Slice(remove, func(i, j int) bool { return remove[i].Field < remove[j].Field })
	return add, remove
}

// partitionTypeToAPI encodes a partition strategy the way the API expects it:
// a bare string for `value`/`prefix` and an object for `hash`.
func partitionTypeToAPI(kind string, buckets types.Int64) any {
	switch kind {
	case "hash":
		n := int64(16)
		if !buckets.IsNull() && !buckets.IsUnknown() {
			n = buckets.ValueInt64()
		}
		return map[string]any{"hash": n}
	case "prefix":
		return "prefix"
	default:
		return "value"
	}
}

// partitionTypeFromAPI decodes the API representation back into a strategy name
// and, for hash partitioning, the bucket count.
func partitionTypeFromAPI(v any) (string, types.Int64) {
	switch t := v.(type) {
	case string:
		return strings.ToLower(t), types.Int64Null()
	case map[string]any:
		if raw, ok := t["hash"]; ok {
			if f, ok := raw.(float64); ok {
				return "hash", types.Int64Value(int64(f))
			}
			return "hash", types.Int64Null()
		}
	}
	return "value", types.Int64Null()
}

func samePartitionType(a, b any) bool {
	aKind, aBuckets := partitionTypeFromAPI(a)
	bKind, bBuckets := partitionTypeFromAPI(b)
	if aKind != bKind {
		return false
	}
	if aKind != "hash" {
		return true
	}
	// A planned hash key always carries an explicit bucket count, so a null
	// count on either side means "whatever the server chose".
	if aBuckets.IsNull() || bBuckets.IsNull() {
		return true
	}
	return aBuckets.ValueInt64() == bBuckets.ValueInt64()
}

// ---------------------------------------------------------------------------
// State mapping
// ---------------------------------------------------------------------------

func (r *StreamResource) readInto(ctx context.Context, org, streamType, name string, model *StreamResourceModel, diags *diag.Diagnostics) {
	stream, err := r.client.GetStream(ctx, org, streamType, name)
	if err != nil {
		diags.AddError("Error reading stream after write", err.Error())
		return
	}
	if stream == nil {
		diags.AddError("Stream not found after write", fmt.Sprintf("Stream %q of type %q was not found in org %q after being written.", name, streamType, org))
		return
	}
	r.applyStreamToModel(ctx, stream, model, diags)
}

func (r *StreamResource) applyStreamToModel(ctx context.Context, stream *StreamAPI, model *StreamResourceModel, diags *diag.Diagnostics) {
	s := stream.Settings

	model.DataRetention = types.Int64Value(s.DataRetention)
	model.MaxQueryRange = types.Int64Value(s.MaxQueryRange)
	if s.FlattenLevel != nil {
		model.FlattenLevel = types.Int64Value(*s.FlattenLevel)
	} else {
		model.FlattenLevel = types.Int64Null()
	}
	model.StoreOriginalData = types.BoolValue(s.StoreOriginalData)
	model.ApproxPartition = types.BoolValue(s.ApproxPartition)
	model.IndexOriginalData = types.BoolValue(s.IndexOriginalData)
	model.IndexAllValues = types.BoolValue(s.IndexAllValues)
	model.EnableDistinctFields = types.BoolValue(s.EnableDistinctFields)
	model.EnableLogPatternsExtraction = types.BoolValue(s.EnableLogPatternsExtraction)

	// These are reconciled rather than overwritten: OpenObserve derives some
	// list settings from others, so the server's view can legitimately be a
	// superset of what was configured.
	model.FullTextSearchKeys = reconcileStringSet(ctx, model.FullTextSearchKeys, s.FullTextSearchKeys, diags)
	model.IndexFields = reconcileStringSet(ctx, model.IndexFields, s.IndexFields, diags)
	model.BloomFilterFields = reconcileStringSet(ctx, model.BloomFilterFields, s.BloomFilterFields, diags)
	model.DefinedSchemaFields = reconcileStringSet(ctx, model.DefinedSchemaFields, s.DefinedSchemaFields, diags)

	distinct := make([]string, 0, len(s.DistinctValueFields))
	for _, d := range s.DistinctValueFields {
		distinct = append(distinct, d.Name)
	}
	model.DistinctValueFields = reconcileStringSet(ctx, model.DistinctValueFields, distinct, diags)

	model.PartitionKeys = reconcilePartitionKeys(ctx, model.PartitionKeys, s.PartitionKeys, diags)

	storageType := s.StorageType
	if storageType == "" {
		storageType = stream.StorageType
	}
	model.StorageType = types.StringValue(storageType)

	fieldValues := make([]attr.Value, 0, len(stream.Schema))
	for _, f := range stream.Schema {
		fieldValues = append(fieldValues, objectValue(streamSchemaFieldAttrTypes, map[string]attr.Value{
			"name": types.StringValue(f.Name),
			"type": types.StringValue(f.Type),
		}, diags))
	}
	fieldList, d := types.ListValue(types.ObjectType{AttrTypes: streamSchemaFieldAttrTypes}, fieldValues)
	diags.Append(d...)
	model.Schema = fieldList
}

func streamID(orgID, streamType, name string) string {
	return fmt.Sprintf("%s/%s/%s", orgID, streamType, name)
}

// reconcilePartitionKeys decides what the partition_keys attribute should hold
// after a write or a read.
//
// A retired partition key is kept by the server with `disabled` set, and a key
// that was never configured may exist because data was ingested before
// Terraform took over. Neither is drift in the configured keys, so when every
// configured key is still present the list is rebuilt in the configured order
// and unmanaged keys are left out. Anything else falls back to the server's
// view so the difference is visible.
func reconcilePartitionKeys(ctx context.Context, configured types.List, server PartitionKeyList, diags *diag.Diagnostics) types.List {
	serverList := partitionKeysToList(server, diags)
	if configured.IsNull() || configured.IsUnknown() {
		return serverList
	}

	var want []StreamPartitionKeyModel
	diags.Append(configured.ElementsAs(ctx, &want, false)...)
	if diags.HasError() {
		return serverList
	}

	byField := make(map[string]StreamPartitionAPI, len(server))
	for _, pk := range server {
		byField[pk.Field] = pk
	}

	values := make([]attr.Value, 0, len(want))
	for _, w := range want {
		have, ok := byField[w.Field.ValueString()]
		if !ok || have.Disabled {
			return serverList
		}
		kind, buckets := partitionTypeFromAPI(have.Types)
		if !w.Type.IsNull() && !w.Type.IsUnknown() && w.Type.ValueString() != kind {
			return serverList
		}
		values = append(values, objectValue(streamPartitionKeyAttrTypes, map[string]attr.Value{
			"field":        types.StringValue(have.Field),
			"type":         types.StringValue(kind),
			"hash_buckets": buckets,
			"disabled":     types.BoolValue(have.Disabled),
		}, diags))
	}

	list, d := types.ListValue(types.ObjectType{AttrTypes: streamPartitionKeyAttrTypes}, values)
	diags.Append(d...)
	return list
}
