package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &RumTokenDataSource{}
	_ datasource.DataSourceWithConfigure = &RumTokenDataSource{}
)

// NewRumTokenDataSource returns a factory for the openobserve_rum_token data
// source.
func NewRumTokenDataSource() datasource.DataSource { return &RumTokenDataSource{} }

// RumTokenDataSource reads the provider user's RUM token for an organization.
type RumTokenDataSource struct{ client *Client }

// RumTokenDataSourceModel holds the state for a RUM token lookup.
type RumTokenDataSourceModel struct {
	ID    types.String `tfsdk:"id"`
	OrgID types.String `tfsdk:"org_id"`
	User  types.String `tfsdk:"user"`
	Token types.String `tfsdk:"token"`
}

func (d *RumTokenDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rum_token"
}

func (d *RumTokenDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the RUM token of the user the provider signs in as, for one organization.\n\n" +
			"The browser RUM intake (`/rum/v1/{org_id}/...`) accepts only a user's RUM token, not an " +
			"ingestion token. OpenObserve gives every org membership its own RUM token and returns it only " +
			"to that signed-in user, so this data source can only read the provider user's token.\n\n" +
			"For the root user the server returns the same token for every organization (root's membership " +
			"lives in `default`), and the RUM intake accepts it for any organization. For any other user it " +
			"returns that membership's own token, which only that organization accepts. To read per-org " +
			"tokens, use a provider alias that signs in as a dedicated member of the organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID, the organization identifier."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"user":   schema.StringAttribute{Computed: true, Description: "The user the token belongs to: the one the provider signs in as."},
			"token": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "The RUM token, the client token a RUM SDK sends as `o2-api-key`.",
			},
		},
	}
}

func (d *RumTokenDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *RumTokenDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RumTokenDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	tok, err := d.client.GetRumToken(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error reading RUM token", err.Error())
		return
	}
	if tok.RumToken == nil || *tok.RumToken == "" {
		resp.Diagnostics.AddError(
			"No RUM token",
			fmt.Sprintf("OpenObserve returned no RUM token for user %q in organization %q.", tok.User, org),
		)
		return
	}

	data.ID = types.StringValue(org)
	data.OrgID = types.StringValue(org)
	data.User = types.StringValue(tok.User)
	data.Token = types.StringValue(*tok.RumToken)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
