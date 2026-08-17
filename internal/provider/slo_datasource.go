package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &SloDataSource{}
	_ datasource.DataSourceWithConfigure = &SlosDataSource{}
)

// sloStatusAttrTypes describes the measurement attached to an SLO.
var sloStatusAttrTypes = map[string]attr.Type{
	"coverage":               types.Float64Type,
	"no_data":                types.BoolType,
	"sli":                    types.Float64Type,
	"error_budget_remaining": types.Float64Type,
	"burn_rate":              types.Float64Type,
	"time_to_exhaust_secs":   types.Int64Type,
	"good":                   types.Float64Type,
	"total":                  types.Float64Type,
	"covered_slices":         types.Int64Type,
	"computed_at":            types.Int64Type,
}

// sloStatusSchema is the shared status block for both SLO data sources.
func sloStatusSchema() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"coverage": schema.Float64Attribute{
			Computed:    true,
			Description: "Fraction of the window actually measured, from 0 to 1.",
		},
		"no_data": schema.BoolAttribute{
			Computed: true,
			Description: "True when coverage sits below the floor. The objective is then frozen, neither healthy " +
				"nor breached, and every derived figure below is null.",
		},
		"sli": schema.Float64Attribute{
			Computed:    true,
			Description: "Measured indicator as a percentage. Null while frozen or not yet measured.",
		},
		"error_budget_remaining": schema.Float64Attribute{
			Computed: true,
			Description: "Percentage of the error budget still unspent. Goes negative once the budget is " +
				"overspent, which is deliberate: -80% is what you need to see after burning 180% of the budget.",
		},
		"burn_rate": schema.Float64Attribute{
			Computed:    true,
			Description: "Current burn rate, in multiples of the budget-neutral rate.",
		},
		"time_to_exhaust_secs": schema.Int64Attribute{
			Computed: true,
			Description: "Seconds until the budget is exhausted at the current burn. Null when the burn is at or " +
				"below neutral, because nothing is being exhausted.",
		},
		"good":           schema.Float64Attribute{Computed: true, Description: "Good events or slices in the window."},
		"total":          schema.Float64Attribute{Computed: true, Description: "Total events or slices in the window."},
		"covered_slices": schema.Int64Attribute{Computed: true, Description: "Number of slices that produced a measurement."},
		"computed_at":    schema.Int64Attribute{Computed: true, Description: "When the measurement was last recomputed, in microseconds."},
	}
}

// sloStatusValue converts a status payload into its object value.
func sloStatusValue(status *SloStatusAPI, diags *diag.Diagnostics) types.Object {
	if status == nil {
		return types.ObjectNull(sloStatusAttrTypes)
	}
	return objectValue(sloStatusAttrTypes, map[string]attr.Value{
		"coverage":               types.Float64Value(status.Coverage),
		"no_data":                types.BoolValue(status.NoData),
		"sli":                    float64FromPtr(status.SLI),
		"error_budget_remaining": float64FromPtr(status.ErrorBudgetRemaining),
		"burn_rate":              float64FromPtr(status.BurnRate),
		"time_to_exhaust_secs":   int64FromPtr(status.TimeToExhaustSecs),
		"good":                   types.Float64Value(status.Good),
		"total":                  types.Float64Value(status.Total),
		"covered_slices":         types.Int64Value(status.CoveredSlices),
		"computed_at":            int64FromPtr(status.ComputedAt),
	}, diags)
}

// ---------------------------------------------------------------------------
// Single SLO
// ---------------------------------------------------------------------------

// NewSloDataSource returns a factory for the openobserve_slo data source.
func NewSloDataSource() datasource.DataSource { return &SloDataSource{} }

// SloDataSource reads a single SLO by ID or by name.
type SloDataSource struct{ client *Client }

// SloDataSourceModel holds the state for an SLO lookup.
type SloDataSourceModel struct {
	ID          types.String  `tfsdk:"id"`
	OrgID       types.String  `tfsdk:"org_id"`
	SloID       types.String  `tfsdk:"slo_id"`
	Name        types.String  `tfsdk:"name"`
	FolderID    types.String  `tfsdk:"folder_id"`
	Description types.String  `tfsdk:"description"`
	SliType     types.String  `tfsdk:"sli_type"`
	Target      types.Float64 `tfsdk:"target"`
	WindowSecs  types.Int64   `tfsdk:"window_secs"`
	SliceSecs   types.Int64   `tfsdk:"slice_interval_secs"`
	GroupBy     types.List    `tfsdk:"group_by"`
	Tags        types.Set     `tfsdk:"tags"`
	Enabled     types.Bool    `tfsdk:"enabled"`
	Owner       types.String  `tfsdk:"owner"`
	Status      types.Object  `tfsdk:"status"`
}

func (d *SloDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slo"
}

func (d *SloDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing service level objective, looked up either by `slo_id` or by `name`, along " +
			"with its current measurement.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{slo_id}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"slo_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "SLO identifier. Supply this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "SLO name. Supply this or `slo_id`.",
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Restrict a name lookup to one alert folder. All folders are searched when omitted.",
			},
			"description":         schema.StringAttribute{Computed: true, Description: "What this objective covers."},
			"sli_type":            schema.StringAttribute{Computed: true, Description: "Indicator type: `count`, `time_slice`, or `alert`."},
			"target":              schema.Float64Attribute{Computed: true, Description: "Target availability as a percentage."},
			"window_secs":         schema.Int64Attribute{Computed: true, Description: "Rolling window in seconds."},
			"slice_interval_secs": schema.Int64Attribute{Computed: true, Description: "Slice width in seconds."},
			"group_by": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Columns splitting the objective into per-group measurements.",
			},
			"tags": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Selection tags on the objective.",
			},
			"enabled": schema.BoolAttribute{Computed: true, Description: "Whether the objective is being evaluated."},
			"owner":   schema.StringAttribute{Computed: true, Description: "Objective owner."},
			"status": schema.SingleNestedAttribute{
				Computed: true,
				Description: "Current measurement. Null until the first evaluation pass has run. \"Not yet " +
					"measured\" and \"measured as zero\" are different answers.",
				Attributes: sloStatusSchema(),
			},
		},
	}
}

func (d *SloDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *SloDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SloDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The status only comes from the list endpoint, so a lookup by either
	// selector resolves through the list and keeps the measurement.
	slos, err := d.client.ListSlos(ctx, org, data.FolderID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading SLOs", err.Error())
		return
	}

	sloID := data.SloID.ValueString()
	name := data.Name.ValueString()
	if sloID == "" && name == "" {
		resp.Diagnostics.AddError(
			"Missing SLO selector",
			"Set either `slo_id` or `name` to identify the SLO.",
		)
		return
	}

	var found *SloListItemAPI
	for i := range slos {
		if (sloID != "" && slos[i].ID == sloID) || (sloID == "" && slos[i].Name == name) {
			found = &slos[i]
			break
		}
	}
	if found == nil {
		resp.Diagnostics.AddError(
			"SLO not found",
			fmt.Sprintf("No SLO matching the given selector exists in org %q.", org),
		)
		return
	}

	data.OrgID = types.StringValue(org)
	data.SloID = types.StringValue(found.ID)
	data.ID = types.StringValue(sloResourceID(org, found.ID))
	data.Name = types.StringValue(found.Name)
	data.FolderID = types.StringValue(found.FolderID)
	data.Description = types.StringValue(found.Description)
	data.SliType = types.StringValue(found.SliType)
	data.Target = types.Float64Value(found.Target)
	data.WindowSecs = types.Int64Value(found.WindowSecs)
	data.SliceSecs = types.Int64Value(found.SliceSecs)
	data.GroupBy = listFromStrings(ctx, found.GroupBy, &resp.Diagnostics)
	data.Tags = setFromStrings(ctx, found.Tags, &resp.Diagnostics)
	data.Enabled = types.BoolValue(found.Enabled)
	data.Owner = stringFromPtr(found.Owner)
	data.Status = sloStatusValue(found.Status, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// SLO listing
// ---------------------------------------------------------------------------

// NewSlosDataSource returns a factory for the openobserve_slos data source.
func NewSlosDataSource() datasource.DataSource { return &SlosDataSource{} }

// SlosDataSource lists the SLOs of an organization.
type SlosDataSource struct{ client *Client }

// SlosDataSourceModel holds the state for an SLO listing.
type SlosDataSourceModel struct {
	ID       types.String `tfsdk:"id"`
	OrgID    types.String `tfsdk:"org_id"`
	FolderID types.String `tfsdk:"folder_id"`
	Slos     types.List   `tfsdk:"slos"`
}

var sloSummaryAttrTypes = map[string]attr.Type{
	"slo_id":              types.StringType,
	"name":                types.StringType,
	"folder_id":           types.StringType,
	"description":         types.StringType,
	"sli_type":            types.StringType,
	"target":              types.Float64Type,
	"window_secs":         types.Int64Type,
	"slice_interval_secs": types.Int64Type,
	"enabled":             types.BoolType,
	"owner":               types.StringType,
	"status":              types.ObjectType{AttrTypes: sloStatusAttrTypes},
}

func (d *SlosDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slos"
}

func (d *SlosDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the service level objectives of an OpenObserve organization, each with its current " +
			"measurement. Useful for reporting on budget consumption across every objective at once.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Description: "Restrict the listing to one alert folder. Every folder is listed when omitted.",
			},
			"slos": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The objectives found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"slo_id":              schema.StringAttribute{Computed: true, Description: "SLO identifier."},
						"name":                schema.StringAttribute{Computed: true, Description: "SLO name."},
						"folder_id":           schema.StringAttribute{Computed: true, Description: "Alert folder holding the objective."},
						"description":         schema.StringAttribute{Computed: true, Description: "What this objective covers."},
						"sli_type":            schema.StringAttribute{Computed: true, Description: "Indicator type."},
						"target":              schema.Float64Attribute{Computed: true, Description: "Target availability as a percentage."},
						"window_secs":         schema.Int64Attribute{Computed: true, Description: "Rolling window in seconds."},
						"slice_interval_secs": schema.Int64Attribute{Computed: true, Description: "Slice width in seconds."},
						"enabled":             schema.BoolAttribute{Computed: true, Description: "Whether the objective is being evaluated."},
						"owner":               schema.StringAttribute{Computed: true, Description: "Objective owner."},
						"status": schema.SingleNestedAttribute{
							Computed:    true,
							Description: "Current measurement, or null when the objective has not been measured yet.",
							Attributes:  sloStatusSchema(),
						},
					},
				},
			},
		},
	}
}

func (d *SlosDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *SlosDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SlosDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	slos, err := d.client.ListSlos(ctx, org, data.FolderID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error listing SLOs", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(slos))
	for i := range slos {
		s := &slos[i]
		values = append(values, objectValue(sloSummaryAttrTypes, map[string]attr.Value{
			"slo_id":              types.StringValue(s.ID),
			"name":                types.StringValue(s.Name),
			"folder_id":           types.StringValue(s.FolderID),
			"description":         types.StringValue(s.Description),
			"sli_type":            types.StringValue(s.SliType),
			"target":              types.Float64Value(s.Target),
			"window_secs":         types.Int64Value(s.WindowSecs),
			"slice_interval_secs": types.Int64Value(s.SliceSecs),
			"enabled":             types.BoolValue(s.Enabled),
			"owner":               stringFromPtr(s.Owner),
			"status":              sloStatusValue(s.Status, &resp.Diagnostics),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: sloSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/slos", org))
	data.Slos = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
