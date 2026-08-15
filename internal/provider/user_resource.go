package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &UserResource{}
	_ resource.ResourceWithConfigure   = &UserResource{}
	_ resource.ResourceWithImportState = &UserResource{}
)

// NewUserResource returns a factory for the openobserve_user resource.
func NewUserResource() resource.Resource {
	return &UserResource{}
}

// UserResource manages an OpenObserve user within an organization.
type UserResource struct {
	client *Client
}

// UserResourceModel holds the Terraform state for a user.
type UserResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Email       types.String `tfsdk:"email"`
	FirstName   types.String `tfsdk:"first_name"`
	LastName    types.String `tfsdk:"last_name"`
	Role        types.String `tfsdk:"role"`
	CustomRoles types.Set    `tfsdk:"custom_roles"`
	Password    types.String `tfsdk:"password"`
	IsExternal  types.Bool   `tfsdk:"is_external"`
	CreatedAt   types.Int64  `tfsdk:"created_at"`
}

func (r *UserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *UserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve user and their membership of one organization.\n\n" +
			"To grant one person access to several organizations, declare one `openobserve_user` per organization with " +
			"the same `email`; the first apply creates the account and the rest add memberships.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{email}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the user belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"email": schema.StringAttribute{
				Required:      true,
				Description:   "User email address, used as the login identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"first_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Given name.",
			},
			"last_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Family name.",
			},
			"role": schema.StringAttribute{
				Required: true,
				Description: "Built-in organization role, for example `admin`, `editor`, `viewer`, or `root`. " +
					"The exact set depends on the OpenObserve edition; read it from the `openobserve_user_roles` data source.",
			},
			"custom_roles": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Custom roles (see `openobserve_role`) granted to the user on top of `role`. Enterprise only.\n\n" +
					"~> Assign a role from one side only. Naming a role here and listing this user in that role's " +
					"`users` writes the same grant from two resources, and each will try to remove the other's.",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Password for a newly created user. Changing it updates the user's password.",
			},
			"is_external": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "True when the account is provisioned by an external identity provider (SSO/LDAP).",
			},
			"created_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Creation timestamp in microseconds.",
			},
		},
	}
}

func (r *UserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *UserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	customRoles := stringsFromSet(ctx, plan.CustomRoles, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	email := plan.Email.ValueString()

	createErr := r.client.CreateUser(ctx, org, CreateUserAPI{
		Email:      email,
		FirstName:  plan.FirstName.ValueString(),
		LastName:   plan.LastName.ValueString(),
		Password:   plan.Password.ValueString(),
		Role:       plan.Role.ValueString(),
		CustomRole: customRoles,
		IsExternal: plan.IsExternal.ValueBool(),
	})
	if createErr != nil {
		// The account may already exist globally, in which case the same call
		// against a second organization is a membership grant rather than a
		// create. Fall back to that before surfacing the error.
		if !isAlreadyExists(createErr) {
			resp.Diagnostics.AddError("Error creating user", createErr.Error())
			return
		}
		addErr := r.client.AddUserToOrg(ctx, org, email, AddUserToOrgAPI{
			Role:       plan.Role.ValueString(),
			CustomRole: customRoles,
		})
		// An account that is already a member of this organization is the state
		// being asked for, so adopt it rather than failing the apply.
		if addErr != nil && !isAlreadyExists(addErr) {
			resp.Diagnostics.AddError(
				"Error adding existing user to organization",
				fmt.Sprintf("Creating %s reported that the user already exists, and adding them to %s failed: %s", email, org, addErr.Error()),
			)
			return
		}

		// The existing account keeps whatever role it already had, so the
		// configured role and custom roles are applied explicitly.
		update := UpdateUserAPI{
			FirstName:  optString(plan.FirstName),
			LastName:   optString(plan.LastName),
			Role:       optString(plan.Role),
			CustomRole: customRoles,
		}
		if err := r.client.UpdateUser(ctx, org, email, update); err != nil {
			resp.Diagnostics.AddError("Error updating adopted user", err.Error())
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(userResourceID(org, email))

	// Create answers with only a status message, so the real state comes from a
	// follow-up read.
	r.readInto(ctx, org, email, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	user, err := r.client.GetUser(ctx, org, state.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading user", err.Error())
		return
	}
	if user == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(userResourceID(org, user.Email))
	r.applyUserToModel(user, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	customRoles := stringsFromSet(ctx, plan.CustomRoles, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	email := plan.Email.ValueString()

	update := UpdateUserAPI{
		FirstName:  optString(plan.FirstName),
		LastName:   optString(plan.LastName),
		Role:       optString(plan.Role),
		CustomRole: customRoles,
	}

	// The password is only sent when it actually changed, so an unchanged
	// apply never rewrites the user's credentials.
	if !plan.Password.IsNull() && plan.Password.ValueString() != "" &&
		plan.Password.ValueString() != state.Password.ValueString() {
		update.ChangePassword = true
		newPassword := plan.Password.ValueString()
		update.NewPassword = &newPassword
		if !state.Password.IsNull() && state.Password.ValueString() != "" {
			oldPassword := state.Password.ValueString()
			update.OldPassword = &oldPassword
		}
	}

	if err := r.client.UpdateUser(ctx, org, email, update); err != nil {
		resp.Diagnostics.AddError("Error updating user", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(userResourceID(org, email))
	r.readInto(ctx, org, email, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *UserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteUser(ctx, org, state.Email.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting user", err.Error())
	}
}

// ImportState supports: terraform import openobserve_user.example default/user@example.com
func (r *UserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{email}`, for example `default/user@example.com`.",
		)
		return
	}

	user, err := r.client.GetUser(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading user during import", err.Error())
		return
	}
	if user == nil {
		resp.Diagnostics.AddError("User not found", fmt.Sprintf("User %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := UserResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		Email:       types.StringValue(user.Email),
		CustomRoles: types.SetNull(types.StringType),
		Password:    types.StringNull(),
	}
	r.applyUserToModel(user, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserResource) readInto(ctx context.Context, org, email string, model *UserResourceModel, diags *diag.Diagnostics) {
	user, err := r.client.GetUser(ctx, org, email)
	if err != nil {
		diags.AddError("Error reading user after write", err.Error())
		return
	}
	if user == nil {
		diags.AddError("User not found after write", fmt.Sprintf("User %q was not found in org %q after being written.", email, org))
		return
	}
	r.applyUserToModel(user, model)
}

func (r *UserResource) applyUserToModel(user *UserAPI, model *UserResourceModel) {
	model.Email = types.StringValue(user.Email)
	model.FirstName = types.StringValue(user.FirstName)
	model.LastName = types.StringValue(user.LastName)
	model.IsExternal = types.BoolValue(user.IsExternal)
	model.CreatedAt = types.Int64Value(user.CreatedAt)
	// The role is only overwritten from the server when it reports one, since
	// a custom-role-only user comes back with an empty base role.
	if user.Role != "" {
		model.Role = types.StringValue(user.Role)
	}
}

func userResourceID(orgID, email string) string {
	return fmt.Sprintf("%s/%s", orgID, email)
}
