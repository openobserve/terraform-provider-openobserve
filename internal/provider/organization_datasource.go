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
	_ datasource.DataSource              = &OrganizationDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationDataSource{}
	_ datasource.DataSource              = &OrganizationsDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationsDataSource{}
)

// NewOrganizationDataSource returns a factory for the openobserve_organization data source.
func NewOrganizationDataSource() datasource.DataSource {
	return &OrganizationDataSource{}
}

// NewOrganizationsDataSource returns a factory for the openobserve_organizations data source.
func NewOrganizationsDataSource() datasource.DataSource {
	return &OrganizationsDataSource{}
}

// OrganizationDataSource reads a single organization.
type OrganizationDataSource struct {
	client *Client
}

// OrganizationsDataSource lists every visible organization.
type OrganizationsDataSource struct {
	client *Client
}

// OrganizationDataSourceModel holds the state for a single organization lookup.
type OrganizationDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	Identifier      types.String `tfsdk:"identifier"`
	Name            types.String `tfsdk:"name"`
	OrgType         types.String `tfsdk:"org_type"`
	UserEmail       types.String `tfsdk:"user_email"`
	IngestThreshold types.Int64  `tfsdk:"ingest_threshold"`
	SearchThreshold types.Int64  `tfsdk:"search_threshold"`
}

// OrganizationsDataSourceModel holds the state for an organization listing.
type OrganizationsDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	Organizations types.List   `tfsdk:"organizations"`
}

var organizationAttrTypes = map[string]attr.Type{
	"identifier": types.StringType,
	"name":       types.StringType,
	"org_type":   types.StringType,
	"user_email": types.StringType,
}

func (d *OrganizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (d *OrganizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing OpenObserve organization by identifier.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Data source ID, same as `identifier`."},
			"identifier": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization identifier to look up. Defaults to the provider's `org_id`.",
			},
			"name":             schema.StringAttribute{Computed: true, Description: "Display name."},
			"org_type":         schema.StringAttribute{Computed: true, Description: "Organization type."},
			"user_email":       schema.StringAttribute{Computed: true, Description: "Email of the organization owner."},
			"ingest_threshold": schema.Int64Attribute{Computed: true, Description: "Ingestion quota threshold."},
			"search_threshold": schema.Int64Attribute{Computed: true, Description: "Search quota threshold."},
		},
	}
}

func (d *OrganizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *OrganizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data OrganizationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identifier := requireOrg(d.client, data.Identifier, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	org, err := d.client.GetOrganization(ctx, identifier)
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}
	if org == nil {
		resp.Diagnostics.AddError("Organization not found", fmt.Sprintf("No organization with identifier %q exists.", identifier))
		return
	}

	data.ID = types.StringValue(org.Identifier)
	data.Identifier = types.StringValue(org.Identifier)
	data.Name = types.StringValue(org.Name)
	data.OrgType = types.StringValue(org.OrgType)
	data.UserEmail = types.StringValue(org.UserEmail)
	data.IngestThreshold = types.Int64Value(org.IngestThreshold)
	data.SearchThreshold = types.Int64Value(org.SearchThreshold)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (d *OrganizationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organizations"
}

func (d *OrganizationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the OpenObserve organizations the configured credentials can see.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"organizations": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The organizations found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"identifier": schema.StringAttribute{Computed: true, Description: "Organization identifier."},
						"name":       schema.StringAttribute{Computed: true, Description: "Display name."},
						"org_type":   schema.StringAttribute{Computed: true, Description: "Organization type."},
						"user_email": schema.StringAttribute{Computed: true, Description: "Email of the organization owner."},
					},
				},
			},
		},
	}
}

func (d *OrganizationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *OrganizationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data OrganizationsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgs, err := d.client.ListOrganizations(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing organizations", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(orgs))
	for _, o := range orgs {
		values = append(values, objectValue(organizationAttrTypes, map[string]attr.Value{
			"identifier": types.StringValue(o.Identifier),
			"name":       types.StringValue(o.Name),
			"org_type":   types.StringValue(o.OrgType),
			"user_email": types.StringValue(o.UserEmail),
		}, &resp.Diagnostics))
	}

	list, diags := types.ListValue(types.ObjectType{AttrTypes: organizationAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.ID = types.StringValue("organizations")
	data.Organizations = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
