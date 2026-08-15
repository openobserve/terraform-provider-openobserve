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
	_ datasource.DataSourceWithConfigure = &UserDataSource{}
	_ datasource.DataSourceWithConfigure = &UsersDataSource{}
	_ datasource.DataSourceWithConfigure = &UserRolesDataSource{}
	_ datasource.DataSourceWithConfigure = &ServiceAccountsDataSource{}
	_ datasource.DataSourceWithConfigure = &RoleDataSource{}
	_ datasource.DataSourceWithConfigure = &RolesDataSource{}
	_ datasource.DataSourceWithConfigure = &GroupDataSource{}
	_ datasource.DataSourceWithConfigure = &GroupsDataSource{}
	_ datasource.DataSourceWithConfigure = &ResourcesDataSource{}
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// NewUserDataSource returns a factory for the openobserve_user data source.
func NewUserDataSource() datasource.DataSource { return &UserDataSource{} }

// UserDataSource reads a single user.
type UserDataSource struct{ client *Client }

// UserDataSourceModel holds the state for a single user lookup.
type UserDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Email       types.String `tfsdk:"email"`
	FirstName   types.String `tfsdk:"first_name"`
	LastName    types.String `tfsdk:"last_name"`
	Role        types.String `tfsdk:"role"`
	IsExternal  types.Bool   `tfsdk:"is_external"`
	CreatedAt   types.Int64  `tfsdk:"created_at"`
	CustomRoles types.Set    `tfsdk:"custom_roles"`
	Groups      types.Set    `tfsdk:"groups"`
}

func (d *UserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *UserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing OpenObserve user, including the custom roles and groups they hold.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{email}`."},
			"org_id":      schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"email":       schema.StringAttribute{Required: true, Description: "Email address of the user to look up."},
			"first_name":  schema.StringAttribute{Computed: true, Description: "Given name."},
			"last_name":   schema.StringAttribute{Computed: true, Description: "Family name."},
			"role":        schema.StringAttribute{Computed: true, Description: "Built-in organization role."},
			"is_external": schema.BoolAttribute{Computed: true, Description: "True when the account comes from an external identity provider."},
			"created_at":  schema.Int64Attribute{Computed: true, Description: "Creation timestamp in microseconds."},
			"custom_roles": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Custom roles granted to the user. Empty outside Enterprise.",
			},
			"groups": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Groups the user belongs to. Empty outside Enterprise.",
			},
		},
	}
}

func (d *UserDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *UserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data UserDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	email := data.Email.ValueString()

	user, err := d.client.GetUser(ctx, org, email)
	if err != nil {
		resp.Diagnostics.AddError("Error reading user", err.Error())
		return
	}
	if user == nil {
		resp.Diagnostics.AddError("User not found", fmt.Sprintf("User %q not found in org %q.", email, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(userResourceID(org, user.Email))
	data.FirstName = types.StringValue(user.FirstName)
	data.LastName = types.StringValue(user.LastName)
	data.Role = types.StringValue(user.Role)
	data.IsExternal = types.BoolValue(user.IsExternal)
	data.CreatedAt = types.Int64Value(user.CreatedAt)

	// Custom roles and groups only exist on Enterprise builds; a failure there
	// should leave the rest of the lookup usable rather than fail the plan.
	roles, err := d.client.GetUserRoles(ctx, org, email)
	if err != nil {
		roles = nil
	}
	data.CustomRoles = setFromStrings(ctx, roles, &resp.Diagnostics)

	groups, err := d.client.GetUserGroups(ctx, org, email)
	if err != nil {
		groups = nil
	}
	data.Groups = setFromStrings(ctx, groups, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewUsersDataSource returns a factory for the openobserve_users data source.
func NewUsersDataSource() datasource.DataSource { return &UsersDataSource{} }

// UsersDataSource lists the users of an organization.
type UsersDataSource struct{ client *Client }

// UsersDataSourceModel holds the state for a user listing.
type UsersDataSourceModel struct {
	ID    types.String `tfsdk:"id"`
	OrgID types.String `tfsdk:"org_id"`
	Users types.List   `tfsdk:"users"`
}

var userSummaryAttrTypes = map[string]attr.Type{
	"email":       types.StringType,
	"first_name":  types.StringType,
	"last_name":   types.StringType,
	"role":        types.StringType,
	"is_external": types.BoolType,
	"created_at":  types.Int64Type,
}

func (d *UsersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

func (d *UsersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the users of an OpenObserve organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"users": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "The users found.",
				NestedObject: schema.NestedAttributeObject{Attributes: userSummarySchema()},
			},
		},
	}
}

func userSummarySchema() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"email":       schema.StringAttribute{Computed: true, Description: "Email address."},
		"first_name":  schema.StringAttribute{Computed: true, Description: "Given name."},
		"last_name":   schema.StringAttribute{Computed: true, Description: "Family name."},
		"role":        schema.StringAttribute{Computed: true, Description: "Built-in organization role."},
		"is_external": schema.BoolAttribute{Computed: true, Description: "True when the account comes from an external identity provider."},
		"created_at":  schema.Int64Attribute{Computed: true, Description: "Creation timestamp in microseconds."},
	}
}

func (d *UsersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *UsersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data UsersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	users, err := d.client.ListUsers(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing users", err.Error())
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/users", org))
	data.Users = usersToList(users, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func usersToList(users []UserAPI, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0, len(users))
	for _, u := range users {
		values = append(values, objectValue(userSummaryAttrTypes, map[string]attr.Value{
			"email":       types.StringValue(u.Email),
			"first_name":  types.StringValue(u.FirstName),
			"last_name":   types.StringValue(u.LastName),
			"role":        types.StringValue(u.Role),
			"is_external": types.BoolValue(u.IsExternal),
			"created_at":  types.Int64Value(u.CreatedAt),
		}, diags))
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: userSummaryAttrTypes}, values)
	diags.Append(d...)
	return list
}

// NewUserRolesDataSource returns a factory for the openobserve_user_roles data source.
func NewUserRolesDataSource() datasource.DataSource { return &UserRolesDataSource{} }

// UserRolesDataSource lists the built-in roles the server accepts.
type UserRolesDataSource struct{ client *Client }

// UserRolesDataSourceModel holds the state for a built-in role listing.
type UserRolesDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Roles       types.List   `tfsdk:"roles"`
	RoleOptions types.List   `tfsdk:"role_options"`
}

var userRoleOptionAttrTypes = map[string]attr.Type{
	"label": types.StringType,
	"value": types.StringType,
}

func (d *UserRolesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_roles"
}

func (d *UserRolesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the built-in user roles this OpenObserve deployment accepts. The set differs between the " +
			"open-source and Enterprise editions, so read it here rather than hard-coding role names.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"roles": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Built-in role names, for example `admin`, `editor`, `viewer`. These are the values accepted by `openobserve_user.role`.",
			},
			"role_options": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The same roles with their display labels.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"label": schema.StringAttribute{Computed: true, Description: "Human-readable role name."},
						"value": schema.StringAttribute{Computed: true, Description: "Value to pass to `openobserve_user.role`."},
					},
				},
			},
		},
	}
}

func (d *UserRolesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *UserRolesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data UserRolesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	roles, err := d.client.ListUserRoles(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing user roles", err.Error())
		return
	}

	names := make([]string, 0, len(roles))
	values := make([]attr.Value, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Value)
		values = append(values, objectValue(userRoleOptionAttrTypes, map[string]attr.Value{
			"label": types.StringValue(r.Label),
			"value": types.StringValue(r.Value),
		}, &resp.Diagnostics))
	}
	options, diags := types.ListValue(types.ObjectType{AttrTypes: userRoleOptionAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/users/roles", org))
	data.Roles = listFromStrings(ctx, names, &resp.Diagnostics)
	data.RoleOptions = options

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Service accounts
// ---------------------------------------------------------------------------

// NewServiceAccountsDataSource returns a factory for the openobserve_service_accounts data source.
func NewServiceAccountsDataSource() datasource.DataSource { return &ServiceAccountsDataSource{} }

// ServiceAccountsDataSource lists the service accounts of an organization.
type ServiceAccountsDataSource struct{ client *Client }

// ServiceAccountsDataSourceModel holds the state for a service account listing.
type ServiceAccountsDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	OrgID           types.String `tfsdk:"org_id"`
	ServiceAccounts types.List   `tfsdk:"service_accounts"`
}

func (d *ServiceAccountsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_accounts"
}

func (d *ServiceAccountsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the service accounts of an OpenObserve organization. API tokens are never returned by this " +
			"endpoint, so none appear here.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"service_accounts": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "The service accounts found.",
				NestedObject: schema.NestedAttributeObject{Attributes: userSummarySchema()},
			},
		},
	}
}

func (d *ServiceAccountsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *ServiceAccountsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ServiceAccountsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	accounts, err := d.client.ListServiceAccounts(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing service accounts", err.Error())
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/service_accounts", org))
	data.ServiceAccounts = usersToList(accounts, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// NewRoleDataSource returns a factory for the openobserve_role data source.
func NewRoleDataSource() datasource.DataSource { return &RoleDataSource{} }

// RoleDataSource reads a single custom role.
type RoleDataSource struct{ client *Client }

// RoleDataSourceModel holds the state for a single role lookup.
type RoleDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Name        types.String `tfsdk:"name"`
	Permissions types.Set    `tfsdk:"permissions"`
	Users       types.Set    `tfsdk:"users"`
}

func (d *RoleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (d *RoleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a custom role's permissions and members. Requires OpenObserve Enterprise.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{name}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"name":   schema.StringAttribute{Required: true, Description: "Role name to look up."},
			"permissions": schema.SetNestedAttribute{
				Computed:    true,
				Description: "Permissions granted to the role.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"object":     schema.StringAttribute{Computed: true, Description: "Resource type or specific entity."},
						"permission": schema.StringAttribute{Computed: true, Description: "Grant level."},
					},
				},
			},
			"users": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Emails of users assigned this role directly.",
			},
		},
	}
}

func (d *RoleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *RoleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RoleDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()

	exists, err := d.client.RoleExists(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	if !exists {
		resp.Diagnostics.AddError("Role not found", fmt.Sprintf("Role %q not found in org %q.", name, org))
		return
	}

	permissions, err := d.client.GetRolePermissions(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	users, err := d.client.GetRoleUsers(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(roleResourceID(org, name))
	data.Permissions = permissionsToSet(permissions, &resp.Diagnostics)
	data.Users = setFromStrings(ctx, users, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewRolesDataSource returns a factory for the openobserve_roles data source.
func NewRolesDataSource() datasource.DataSource { return &RolesDataSource{} }

// RolesDataSource lists the custom roles of an organization.
type RolesDataSource struct{ client *Client }

// RolesDataSourceModel holds the state for a role listing.
type RolesDataSourceModel struct {
	ID    types.String `tfsdk:"id"`
	OrgID types.String `tfsdk:"org_id"`
	Roles types.List   `tfsdk:"roles"`
}

func (d *RolesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_roles"
}

func (d *RolesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the custom roles of an OpenObserve organization. Requires OpenObserve Enterprise.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"roles": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Custom role names.",
			},
		},
	}
}

func (d *RolesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *RolesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RolesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	roles, err := d.client.ListRoles(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/roles", org))
	data.Roles = listFromStrings(ctx, roles, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

// NewGroupDataSource returns a factory for the openobserve_group data source.
func NewGroupDataSource() datasource.DataSource { return &GroupDataSource{} }

// GroupDataSource reads a single group.
type GroupDataSource struct{ client *Client }

// GroupDataSourceModel holds the state for a single group lookup.
type GroupDataSourceModel struct {
	ID    types.String `tfsdk:"id"`
	OrgID types.String `tfsdk:"org_id"`
	Name  types.String `tfsdk:"name"`
	Users types.Set    `tfsdk:"users"`
	Roles types.Set    `tfsdk:"roles"`
}

func (d *GroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (d *GroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a group's members and roles. Requires OpenObserve Enterprise.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{name}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"name":   schema.StringAttribute{Required: true, Description: "Group name to look up."},
			"users": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Emails of the group's members.",
			},
			"roles": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Custom roles granted to every member.",
			},
		},
	}
}

func (d *GroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *GroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data GroupDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()

	group, err := d.client.GetGroup(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
		return
	}
	if group == nil {
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("Group %q not found in org %q.", name, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(groupResourceID(org, name))
	data.Users = setFromStrings(ctx, group.Users, &resp.Diagnostics)
	data.Roles = setFromStrings(ctx, group.Roles, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewGroupsDataSource returns a factory for the openobserve_groups data source.
func NewGroupsDataSource() datasource.DataSource { return &GroupsDataSource{} }

// GroupsDataSource lists the groups of an organization.
type GroupsDataSource struct{ client *Client }

// GroupsDataSourceModel holds the state for a group listing.
type GroupsDataSourceModel struct {
	ID     types.String `tfsdk:"id"`
	OrgID  types.String `tfsdk:"org_id"`
	Groups types.List   `tfsdk:"groups"`
}

func (d *GroupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_groups"
}

func (d *GroupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the groups of an OpenObserve organization. Requires OpenObserve Enterprise.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"groups": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Group names.",
			},
		},
	}
}

func (d *GroupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *GroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data GroupsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	groups, err := d.client.ListGroups(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/groups", org))
	data.Groups = listFromStrings(ctx, groups, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Permission resources
// ---------------------------------------------------------------------------

// NewResourcesDataSource returns a factory for the openobserve_resources data source.
func NewResourcesDataSource() datasource.DataSource { return &ResourcesDataSource{} }

// ResourcesDataSource lists the resource types that can appear in a permission.
type ResourcesDataSource struct{ client *Client }

// ResourcesDataSourceModel holds the state for a permission resource listing.
type ResourcesDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	OrgID     types.String `tfsdk:"org_id"`
	Resources types.List   `tfsdk:"resources"`
}

var permissionResourceAttrTypes = map[string]attr.Type{
	"key":          types.StringType,
	"display_name": types.StringType,
	"parent":       types.StringType,
	"visible":      types.BoolType,
	"has_entities": types.BoolType,
}

func (d *ResourcesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_resources"
}

func (d *ResourcesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the resource types that can be named in an `openobserve_role` permission, for example " +
			"`stream`, `dashboard`, or `alert`. Requires OpenObserve Enterprise.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"resources": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The resource types available for permission grants.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key":          schema.StringAttribute{Computed: true, Description: "Identifier used in a permission's `object`, for example `stream`."},
						"display_name": schema.StringAttribute{Computed: true, Description: "Human-readable name."},
						"parent":       schema.StringAttribute{Computed: true, Description: "Parent resource type for hierarchical inheritance."},
						"visible":      schema.BoolAttribute{Computed: true, Description: "Whether the type appears in the permissions UI."},
						"has_entities": schema.BoolAttribute{Computed: true, Description: "Whether permissions can target individual entities, not just the whole type."},
					},
				},
			},
		},
	}
}

func (d *ResourcesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *ResourcesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ResourcesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resources, err := d.client.ListResources(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Permission resources", err))
		return
	}

	values := make([]attr.Value, 0, len(resources))
	for _, r := range resources {
		values = append(values, objectValue(permissionResourceAttrTypes, map[string]attr.Value{
			"key":          types.StringValue(r.Key),
			"display_name": types.StringValue(r.DisplayName),
			"parent":       types.StringValue(r.Parent),
			"visible":      types.BoolValue(r.Visible),
			"has_entities": types.BoolValue(r.HasEntities),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: permissionResourceAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/resources", org))
	data.Resources = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
