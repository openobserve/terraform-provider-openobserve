package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &SyntheticsDataSource{}
	_ datasource.DataSourceWithConfigure = &SyntheticLocationsDataSource{}
	_ datasource.DataSourceWithConfigure = &IngestionTokensDataSource{}
)

// ---------------------------------------------------------------------------
// Synthetics
// ---------------------------------------------------------------------------

// NewSyntheticsDataSource returns a factory for the openobserve_synthetics data source.
func NewSyntheticsDataSource() datasource.DataSource { return &SyntheticsDataSource{} }

// SyntheticsDataSource lists synthetic checks.
type SyntheticsDataSource struct{ client *Client }

// SyntheticsDataSourceModel holds the state for a synthetics listing.
type SyntheticsDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	OrgID      types.String `tfsdk:"org_id"`
	Synthetics types.List   `tfsdk:"synthetics"`
}

var syntheticSummaryAttrTypes = map[string]attr.Type{
	"synthetic_id": types.StringType,
	"name":         types.StringType,
	"description":  types.StringType,
	"type":         types.StringType,
	"target":       types.StringType,
	"enabled":      types.BoolType,
	"folder_id":    types.StringType,
	"locations":    types.ListType{ElemType: types.StringType},
	"config":       types.StringType,
}

func (d *SyntheticsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_synthetics"
}

func (d *SyntheticsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the synthetic checks in an organization.\n\n" +
			"This is the fastest way to work out the `config` document a check type expects: build one in the " +
			"UI, read it back here, and paste the result into `jsonencode()`. It also carries the " +
			"server-assigned identifiers a `terraform import` needs.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"synthetics": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every synthetic check in the organization.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"synthetic_id": schema.StringAttribute{Computed: true, Description: "Server-assigned identifier, which is what `terraform import` takes."},
						"name":         schema.StringAttribute{Computed: true, Description: "Check name."},
						"description":  schema.StringAttribute{Computed: true, Description: "What the check verifies."},
						"type":         schema.StringAttribute{Computed: true, Description: "`http`, `tcp`, `tls`, `ssh`, or `browser`."},
						"target":       schema.StringAttribute{Computed: true, Description: "What is probed."},
						"enabled":      schema.BoolAttribute{Computed: true, Description: "Whether the check runs."},
						"folder_id":    schema.StringAttribute{Computed: true, Description: "Folder holding the check."},
						"locations": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Where the check probes from.",
						},
						"config": schema.StringAttribute{
							Computed:    true,
							Description: "The type-specific settings document, as stored.",
						},
					},
				},
			},
		},
	}
}

func (d *SyntheticsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *SyntheticsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SyntheticsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	checks, err := d.client.ListSynthetics(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing synthetic checks", syntheticErrorDetail(err))
		return
	}

	values := make([]attr.Value, 0, len(checks))
	for _, c := range checks {
		config := types.StringNull()
		if len(c.Config) > 0 {
			config = types.StringValue(string(c.Config))
		}
		values = append(values, objectValue(syntheticSummaryAttrTypes, map[string]attr.Value{
			"synthetic_id": types.StringValue(c.ID),
			"name":         types.StringValue(c.Name),
			"description":  types.StringValue(c.Description),
			"type":         types.StringValue(c.CheckType),
			"target":       types.StringValue(c.Target),
			"enabled":      types.BoolValue(c.Enabled),
			"folder_id":    types.StringValue(c.FolderID),
			"locations":    listFromStrings(ctx, c.Locations, &resp.Diagnostics),
			"config":       config,
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: syntheticSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(org)
	data.Synthetics = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Synthetic locations
// ---------------------------------------------------------------------------

// NewSyntheticLocationsDataSource returns a factory for the
// openobserve_synthetic_locations data source.
func NewSyntheticLocationsDataSource() datasource.DataSource {
	return &SyntheticLocationsDataSource{}
}

// SyntheticLocationsDataSource lists the probe locations available.
type SyntheticLocationsDataSource struct{ client *Client }

// SyntheticLocationsDataSourceModel holds the state for a locations listing.
type SyntheticLocationsDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	OrgID     types.String `tfsdk:"org_id"`
	Locations types.List   `tfsdk:"locations"`
	Names     types.List   `tfsdk:"names"`
	Browsers  types.List   `tfsdk:"browsers"`
	Devices   types.List   `tfsdk:"devices"`
}

var syntheticDeviceAttrTypes = map[string]attr.Type{
	"id":     types.StringType,
	"label":  types.StringType,
	"width":  types.Int64Type,
	"height": types.Int64Type,
}

var syntheticLocationAttrTypes = map[string]attr.Type{
	"location_id": types.StringType,
	"label":       types.StringType,
	"region":      types.StringType,
	"provider":    types.StringType,
	"kind":        types.StringType,
	"enabled":     types.BoolType,
	"status":      types.StringType,
	"live_agents": types.Int64Type,
	"types":       types.ListType{ElemType: types.StringType},
}

func (d *SyntheticLocationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_synthetic_locations"
}

func (d *SyntheticLocationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the probe locations a synthetic check can run from.\n\n" +
			"Locations are registered out of band, by running a probe agent or by OpenObserve for its " +
			"public regions, so they are read here rather than declared. Which ones exist depends on the " +
			"deployment, so naming one that is not configured has a " +
			"check rejected at apply time. Read them rather than hardcoding.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"names": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Just the location identifiers, ready to pass to a check's `locations`.",
			},
			"browsers": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Browsers a `browser` check can run in.",
			},
			"devices": schema.ListNestedAttribute{
				Computed: true,
				Description: "Viewports a `browser` check can emulate. A browser run costs devices times " +
					"attempts, so this is also what decides how expensive a check is.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":     schema.StringAttribute{Computed: true, Description: "Identifier a config references, for example `desktop`."},
						"label":  schema.StringAttribute{Computed: true, Description: "Human-readable name."},
						"width":  schema.Int64Attribute{Computed: true, Description: "Viewport width in pixels."},
						"height": schema.Int64Attribute{Computed: true, Description: "Viewport height in pixels."},
					},
				},
			},
			"locations": schema.ListNestedAttribute{
				Computed: true,
				Description: "Every configured probe location. Empty on a deployment that has not registered " +
					"any, in which case a check has nowhere to run from.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"location_id": schema.StringAttribute{
							Computed:    true,
							Description: "What a check's `locations` references. This is the identifier, not the label.",
						},
						"label":    schema.StringAttribute{Computed: true, Description: "Human-readable name."},
						"region":   schema.StringAttribute{Computed: true, Description: "Region the probe runs in."},
						"provider": schema.StringAttribute{Computed: true, Description: "Who hosts the probe."},
						"kind": schema.StringAttribute{
							Computed:    true,
							Description: "`public` for a location OpenObserve runs, `private` for one of your own agents.",
						},
						"enabled": schema.BoolAttribute{Computed: true, Description: "Whether the location is configured as usable."},
						"status": schema.StringAttribute{
							Computed: true,
							Description: "`online`, `offline`, or `pending`. A private location reads `pending` until " +
								"an agent registers, and a check assigned to it will not run, so this matters as " +
								"much as `enabled`.",
						},
						"live_agents": schema.Int64Attribute{Computed: true, Description: "How many agents are currently serving this location."},
						"types": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Check types runnable here, which for a private location comes from what its agents report.",
						},
					},
				},
			},
		},
	}
}

func (d *SyntheticLocationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *SyntheticLocationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data SyntheticLocationsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	catalog, err := d.client.ListSyntheticLocations(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing synthetic locations", syntheticErrorDetail(err))
		return
	}
	locations := catalog.Locations

	values := make([]attr.Value, 0, len(locations))
	names := make([]string, 0, len(locations))
	for _, l := range locations {
		names = append(names, l.ID)
		values = append(values, objectValue(syntheticLocationAttrTypes, map[string]attr.Value{
			"location_id": types.StringValue(l.ID),
			"label":       types.StringValue(l.Label),
			"region":      types.StringValue(l.Region),
			"provider":    types.StringValue(l.Provider),
			"kind":        types.StringValue(l.Kind),
			"enabled":     types.BoolValue(l.Enabled),
			"status":      types.StringValue(l.Status),
			"live_agents": types.Int64Value(l.LiveAgents),
			"types":       listFromStrings(ctx, l.Types, &resp.Diagnostics),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: syntheticLocationAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(org)
	data.Locations = list
	data.Names = listFromStrings(ctx, names, &resp.Diagnostics)
	data.Browsers = listFromStrings(ctx, catalog.Browsers, &resp.Diagnostics)

	deviceValues := make([]attr.Value, 0, len(catalog.Devices))
	for _, dev := range catalog.Devices {
		deviceValues = append(deviceValues, objectValue(syntheticDeviceAttrTypes, map[string]attr.Value{
			"id":     types.StringValue(dev.ID),
			"label":  types.StringValue(dev.Label),
			"width":  types.Int64Value(dev.Width),
			"height": types.Int64Value(dev.Height),
		}, &resp.Diagnostics))
	}
	devices, deviceDiags := types.ListValue(types.ObjectType{AttrTypes: syntheticDeviceAttrTypes}, deviceValues)
	resp.Diagnostics.Append(deviceDiags...)
	data.Devices = devices

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Ingestion tokens
// ---------------------------------------------------------------------------

// NewIngestionTokensDataSource returns a factory for the
// openobserve_ingestion_tokens data source.
func NewIngestionTokensDataSource() datasource.DataSource { return &IngestionTokensDataSource{} }

// IngestionTokensDataSource lists ingestion tokens.
type IngestionTokensDataSource struct{ client *Client }

// IngestionTokensDataSourceModel holds the state for a token listing.
type IngestionTokensDataSourceModel struct {
	ID     types.String `tfsdk:"id"`
	OrgID  types.String `tfsdk:"org_id"`
	Tokens types.List   `tfsdk:"tokens"`
}

var ingestionTokenAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"description": types.StringType,
	"enabled":     types.BoolType,
	"is_default":  types.BoolType,
	"created_by":  types.StringType,
	"created_at":  types.Int64Type,
}

func (d *IngestionTokensDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ingestion_tokens"
}

func (d *IngestionTokensDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the ingestion tokens in an organization.\n\n" +
			"The token values are deliberately not exposed here. A listing is the wrong place to hand out " +
			"live credentials, and a data source anyone can read would put every token into any state file " +
			"that happened to reference it. Use `openobserve_ingestion_token` to create a token and read its " +
			"value once.\n\n" +
			"What this is for is auditing: which tokens exist, which are still enabled, and which are " +
			"unaccounted for.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"tokens": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every ingestion token in the organization, without its value.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":        schema.StringAttribute{Computed: true, Description: "Token name."},
						"description": schema.StringAttribute{Computed: true, Description: "What the token is for."},
						"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether it still authenticates ingestion."},
						"is_default":  schema.BoolAttribute{Computed: true, Description: "Whether it is the organization's default token."},
						"created_by":  schema.StringAttribute{Computed: true, Description: "Who created it."},
						"created_at":  schema.Int64Attribute{Computed: true, Description: "When it was created, in microseconds."},
					},
				},
			},
		},
	}
}

func (d *IngestionTokensDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *IngestionTokensDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data IngestionTokensDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	tokens, err := d.client.ListIngestionTokens(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing ingestion tokens", ingestionTokenErrorDetail(err))
		return
	}

	values := make([]attr.Value, 0, len(tokens))
	for _, t := range tokens {
		values = append(values, objectValue(ingestionTokenAttrTypes, map[string]attr.Value{
			"name":        types.StringValue(t.Name),
			"description": types.StringValue(t.Description),
			"enabled":     types.BoolValue(t.Enabled),
			"is_default":  types.BoolValue(t.IsDefault),
			"created_by":  types.StringValue(t.CreatedBy),
			"created_at":  types.Int64Value(t.CreatedAt),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: ingestionTokenAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(org)
	data.Tokens = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
