package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &OpenObserveProvider{}
var _ provider.ProviderWithFunctions = &OpenObserveProvider{}

// OpenObserveProvider implements the Terraform provider.
type OpenObserveProvider struct {
	version string
}

// OpenObserveProviderModel maps provider schema attributes to Go types.
type OpenObserveProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	OrgID    types.String `tfsdk:"org_id"`
}

// New returns a provider factory for the given version string.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &OpenObserveProvider{version: version}
	}
}

func (p *OpenObserveProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "openobserve"
	resp.Version = p.version
}

func (p *OpenObserveProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The OpenObserve provider manages streams, dashboards, users, and other resources via the OpenObserve REST API.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Description: "Base URL of the OpenObserve instance (e.g. https://openobserve.example.com). May also be set via OPENOBSERVE_ENDPOINT.",
				Optional:    true,
			},
			"username": schema.StringAttribute{
				Description: "OpenObserve login email. May also be set via OPENOBSERVE_USERNAME.",
				Optional:    true,
				Sensitive:   true,
			},
			"password": schema.StringAttribute{
				Description: "OpenObserve password. May also be set via OPENOBSERVE_PASSWORD.",
				Optional:    true,
				Sensitive:   true,
			},
			"org_id": schema.StringAttribute{
				Description: "Default organization identifier used when a resource does not set org_id explicitly. May also be set via OPENOBSERVE_ORG_ID.",
				Optional:    true,
			},
		},
	}
}

func (p *OpenObserveProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data OpenObserveProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := envOr(data.Endpoint, "OPENOBSERVE_ENDPOINT")
	username := envOr(data.Username, "OPENOBSERVE_USERNAME")
	password := envOr(data.Password, "OPENOBSERVE_PASSWORD")
	orgID := envOr(data.OrgID, "OPENOBSERVE_ORG_ID")

	if endpoint == "" {
		resp.Diagnostics.AddError(
			"Missing OpenObserve endpoint",
			"Set endpoint in the provider block or the OPENOBSERVE_ENDPOINT environment variable.",
		)
	}
	if username == "" {
		resp.Diagnostics.AddError(
			"Missing OpenObserve username",
			"Set username in the provider block or the OPENOBSERVE_USERNAME environment variable.",
		)
	}
	if password == "" {
		resp.Diagnostics.AddError(
			"Missing OpenObserve password",
			"Set password in the provider block or the OPENOBSERVE_PASSWORD environment variable.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	client := newClient(endpoint, username, password, orgID)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *OpenObserveProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewOrganizationResource,
		NewStreamResource,
		NewFolderResource,
		NewDashboardResource,
		NewUserResource,
		NewServiceAccountResource,
		NewRoleResource,
		NewGroupResource,
		NewAlertTemplateResource,
		NewAlertDestinationResource,
		NewAlertResource,
	}
}

func (p *OpenObserveProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewOrganizationDataSource,
		NewOrganizationsDataSource,
		NewStreamDataSource,
		NewStreamsDataSource,
		NewUserDataSource,
		NewUsersDataSource,
		NewUserRolesDataSource,
		NewServiceAccountsDataSource,
		NewRoleDataSource,
		NewRolesDataSource,
		NewGroupDataSource,
		NewGroupsDataSource,
		NewResourcesDataSource,
		NewFolderDataSource,
		NewFoldersDataSource,
		NewDashboardDataSource,
		NewDashboardsDataSource,
		NewAlertTemplateDataSource,
		NewAlertTemplatesDataSource,
		NewAlertDestinationDataSource,
		NewAlertDestinationsDataSource,
		NewAlertDataSource,
		NewAlertsDataSource,
	}
}

func (p *OpenObserveProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{}
}

// envOr returns the provider attribute value when set, otherwise falls back to the named env var.
func envOr(attr types.String, envVar string) string {
	if !attr.IsNull() && !attr.IsUnknown() {
		return attr.ValueString()
	}
	return os.Getenv(envVar)
}
