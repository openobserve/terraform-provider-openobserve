package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &OrganizationDataSource{}

// NewOrganizationDataSource returns a factory for the openobserve_organization data source.
func NewOrganizationDataSource() datasource.DataSource {
	return &OrganizationDataSource{}
}

// OrganizationDataSource reads an OpenObserve organization.
type OrganizationDataSource struct {
	client *Client
}

// OrganizationDataSourceModel holds the Terraform state for the data source.
type OrganizationDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	Identifier types.String `tfsdk:"identifier"`
	Name       types.String `tfsdk:"name"`
	Type       types.Int64  `tfsdk:"type"`
	UserEmail  types.String `tfsdk:"user_email"`
}

func (d *OrganizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (d *OrganizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an OpenObserve organization by its identifier.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Data source ID, same as `identifier`.",
			},
			"identifier": schema.StringAttribute{
				Required:    true,
				Description: "Organization identifier (e.g. `default`).",
			},
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "Human-readable organization name.",
			},
			"type": schema.Int64Attribute{
				Computed:    true,
				Description: "Organization type code returned by the API.",
			},
			"user_email": schema.StringAttribute{
				Computed:    true,
				Description: "Email of the user who created or owns this organization.",
			},
		},
	}
}

func (d *OrganizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *OrganizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data OrganizationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org, err := d.client.GetOrganization(ctx, data.Identifier.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}
	if org == nil {
		resp.Diagnostics.AddError(
			"Organization not found",
			fmt.Sprintf("Organization with identifier %q was not found.", data.Identifier.ValueString()),
		)
		return
	}

	data.ID = data.Identifier
	data.Name = types.StringValue(org.Name)
	data.Type = types.Int64Value(int64(org.Type))
	data.UserEmail = types.StringValue(org.UserEmail)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
