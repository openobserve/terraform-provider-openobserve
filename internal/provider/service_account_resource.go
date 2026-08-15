package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &ServiceAccountResource{}
	_ resource.ResourceWithConfigure   = &ServiceAccountResource{}
	_ resource.ResourceWithImportState = &ServiceAccountResource{}
)

// NewServiceAccountResource returns a factory for the openobserve_service_account resource.
func NewServiceAccountResource() resource.Resource {
	return &ServiceAccountResource{}
}

// ServiceAccountResource manages a non-human account used for automation.
type ServiceAccountResource struct {
	client *Client
}

// ServiceAccountResourceModel holds the Terraform state for a service account.
type ServiceAccountResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Email       types.String `tfsdk:"email"`
	FirstName   types.String `tfsdk:"first_name"`
	LastName    types.String `tfsdk:"last_name"`
	CustomRoles types.Set    `tfsdk:"custom_roles"`
	RotateToken types.String `tfsdk:"rotate_token"`
	Token       types.String `tfsdk:"token"`
	IsSystem    types.Bool   `tfsdk:"is_system"`
	CreatedAt   types.Int64  `tfsdk:"created_at"`
	Description types.String `tfsdk:"description"`
}

func (r *ServiceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *ServiceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve service account: a non-human identity that authenticates with an API token " +
			"instead of a password.\n\n" +
			"The API token is returned only when the account is created or when the token is rotated, so it is stored in " +
			"Terraform state. Treat your state file as a secret.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{email}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the service account belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"email": schema.StringAttribute{
				Required:      true,
				Description:   "Email-shaped identifier for the account, for example `ci-pipeline@example.com`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"first_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Display name for the account.",
			},
			"last_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Secondary display name for the account.",
			},
			"custom_roles": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Custom roles (see `openobserve_role`) granted to the service account. Enterprise only.\n\n" +
					"~> Assign a role from one side only. Naming a role here and listing this account in that role's " +
					"`users` writes the same grant from two resources, and each will try to remove the other's.",
			},
			"rotate_token": schema.StringAttribute{
				Optional: true,
				Description: "Arbitrary value that triggers a token rotation whenever it changes. Set it to a timestamp " +
					"or version string and change it to issue a new token and invalidate the old one.",
			},
			"token": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "API token for the service account. Only populated on creation and after a rotation.",
			},
			"is_system": schema.BoolAttribute{
				Computed:    true,
				Description: "True for system-managed service accounts, which cannot be modified.",
			},
			"created_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Creation timestamp in microseconds.",
			},
			"description": schema.StringAttribute{
				Computed:    true,
				Description: "Server-supplied description of what the account is used for.",
			},
		},
	}
}

func (r *ServiceAccountResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *ServiceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ServiceAccountResourceModel
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

	created, err := r.client.CreateServiceAccount(ctx, org, ServiceAccountAPI{
		Email:     email,
		FirstName: plan.FirstName.ValueString(),
		LastName:  plan.LastName.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating service account", err.Error())
		return
	}

	if len(customRoles) > 0 {
		// Custom roles are attached through the user endpoint; the service
		// account endpoint only accepts profile fields.
		roleUpdate := UpdateUserAPI{CustomRole: customRoles}
		if err := r.client.UpdateUser(ctx, org, email, roleUpdate); err != nil {
			resp.Diagnostics.AddError("Error assigning custom roles to service account", err.Error())
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(userResourceID(org, email))
	plan.Token = types.StringValue(created.Token)

	r.refresh(ctx, org, email, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ServiceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ServiceAccountResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	account, err := r.client.GetServiceAccount(ctx, org, state.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading service account", err.Error())
		return
	}
	if account == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(userResourceID(org, account.Email))
	applyServiceAccountToModel(account, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ServiceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ServiceAccountResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	plannedRoles := stringsFromSet(ctx, plan.CustomRoles, &resp.Diagnostics)
	currentRoles := stringsFromSet(ctx, state.CustomRoles, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	email := plan.Email.ValueString()

	err := r.client.UpdateServiceAccount(ctx, org, email, ServiceAccountAPI{
		FirstName: plan.FirstName.ValueString(),
		LastName:  plan.LastName.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error updating service account", err.Error())
		return
	}

	if !equalStringSlices(plannedRoles, currentRoles) {
		if err := r.client.UpdateUser(ctx, org, email, UpdateUserAPI{CustomRole: plannedRoles}); err != nil {
			resp.Diagnostics.AddError("Error updating service account custom roles", err.Error())
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(userResourceID(org, email))
	plan.Token = state.Token

	// Rotating invalidates the previous token, so it only happens when the
	// rotate_token trigger actually changed.
	if !plan.RotateToken.Equal(state.RotateToken) && !plan.RotateToken.IsNull() {
		rotated, err := r.client.RotateServiceAccountToken(ctx, org, email)
		if err != nil {
			resp.Diagnostics.AddError("Error rotating service account token", err.Error())
			return
		}
		plan.Token = types.StringValue(rotated.Token)
	}

	r.refresh(ctx, org, email, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ServiceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ServiceAccountResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteServiceAccount(ctx, org, state.Email.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting service account", err.Error())
	}
}

// ImportState supports: terraform import openobserve_service_account.ci default/ci@example.com
//
// The API token cannot be recovered after creation, so an imported account has
// an empty `token` until it is rotated.
func (r *ServiceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{email}`, for example `default/ci@example.com`.",
		)
		return
	}

	account, err := r.client.GetServiceAccount(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading service account during import", err.Error())
		return
	}
	if account == nil {
		resp.Diagnostics.AddError("Service account not found", fmt.Sprintf("Service account %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := ServiceAccountResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		Email:       types.StringValue(account.Email),
		CustomRoles: types.SetNull(types.StringType),
		RotateToken: types.StringNull(),
		Token:       types.StringValue(""),
	}
	applyServiceAccountToModel(account, &state)

	resp.Diagnostics.AddWarning(
		"Service account token not imported",
		"OpenObserve never returns an existing API token. `token` is empty for this imported account; "+
			"change `rotate_token` to issue a fresh one.",
	)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ServiceAccountResource) refresh(ctx context.Context, org, email string, model *ServiceAccountResourceModel, diags *diag.Diagnostics) {
	account, err := r.client.GetServiceAccount(ctx, org, email)
	if err != nil {
		diags.AddError("Error reading service account after write", err.Error())
		return
	}
	if account == nil {
		diags.AddError("Service account not found after write", fmt.Sprintf("Service account %q was not found in org %q after being written.", email, org))
		return
	}
	applyServiceAccountToModel(account, model)
}

func applyServiceAccountToModel(account *UserAPI, model *ServiceAccountResourceModel) {
	model.Email = types.StringValue(account.Email)
	model.FirstName = types.StringValue(account.FirstName)
	model.LastName = types.StringValue(account.LastName)
	model.IsSystem = types.BoolValue(account.IsSystem)
	model.CreatedAt = types.Int64Value(account.CreatedAt)
	model.Description = types.StringValue(account.Description)
}

// equalStringSlices reports whether two already-sorted slices hold the same values.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
