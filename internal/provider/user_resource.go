package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &UserResource{}
var _ resource.ResourceWithImportState = &UserResource{}

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
	ID         types.String `tfsdk:"id"`
	OrgID      types.String `tfsdk:"org_id"`
	Email      types.String `tfsdk:"email"`
	FirstName  types.String `tfsdk:"first_name"`
	LastName   types.String `tfsdk:"last_name"`
	Role       types.String `tfsdk:"role"`
	Password   types.String `tfsdk:"password"`
	IsExternal types.Bool   `tfsdk:"is_external"`
}

func (r *UserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *UserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve user within an organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource ID in the format `{org_id}/{email}`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"org_id": schema.StringAttribute{
				Required:    true,
				Description: "Organization the user belongs to.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"email": schema.StringAttribute{
				Required:    true,
				Description: "User email address. Used as the login identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"first_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "User first name.",
			},
			"last_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "User last name.",
			},
			"role": schema.StringAttribute{
				Required:    true,
				Description: "Organization role: `admin`, `editor`, or `viewer`.",
				Validators: []validator.String{
					stringvalidator.OneOf("admin", "editor", "viewer"),
				},
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "User password. Required when creating a new user. Leave unset to manage an existing user without changing their password.",
			},
			"is_external": schema.BoolAttribute{
				Computed:    true,
				Description: "True when the user is managed by an external identity provider (SSO/LDAP).",
			},
		},
	}
}

func (r *UserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *UserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := UserAPI{
		Email:     data.Email.ValueString(),
		FirstName: data.FirstName.ValueString(),
		LastName:  data.LastName.ValueString(),
		Role:      data.Role.ValueString(),
		Password:  data.Password.ValueString(),
	}

	created, err := r.client.CreateUser(ctx, data.OrgID.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating user", err.Error())
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%s/%s", data.OrgID.ValueString(), data.Email.ValueString()))
	data.FirstName = types.StringValue(created.FirstName)
	data.LastName = types.StringValue(created.LastName)
	data.Role = types.StringValue(created.Role)
	data.IsExternal = types.BoolValue(created.IsExternal)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *UserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	user, err := r.client.GetUser(ctx, data.OrgID.ValueString(), data.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading user", err.Error())
		return
	}
	if user == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.FirstName = types.StringValue(user.FirstName)
	data.LastName = types.StringValue(user.LastName)
	data.Role = types.StringValue(user.Role)
	data.IsExternal = types.BoolValue(user.IsExternal)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *UserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data UserResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := UserAPI{
		Email:     data.Email.ValueString(),
		FirstName: data.FirstName.ValueString(),
		LastName:  data.LastName.ValueString(),
		Role:      data.Role.ValueString(),
	}
	if !data.Password.IsNull() {
		apiReq.Password = data.Password.ValueString()
	}

	updated, err := r.client.UpdateUser(ctx, data.OrgID.ValueString(), data.Email.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating user", err.Error())
		return
	}

	data.FirstName = types.StringValue(updated.FirstName)
	data.LastName = types.StringValue(updated.LastName)
	data.Role = types.StringValue(updated.Role)
	data.IsExternal = types.BoolValue(updated.IsExternal)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *UserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data UserResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteUser(ctx, data.OrgID.ValueString(), data.Email.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting user", err.Error())
	}
}

// ImportState supports: terraform import openobserve_user.example default/user@example.com
func (r *UserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be in the format {org_id}/{email}, e.g. default/user@example.com",
		)
		return
	}

	data := UserResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Email: types.StringValue(parts[1]),
	}

	user, err := r.client.GetUser(ctx, data.OrgID.ValueString(), data.Email.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading user during import", err.Error())
		return
	}
	if user == nil {
		resp.Diagnostics.AddError("User not found", fmt.Sprintf("User %q not found in org %q.", parts[1], parts[0]))
		return
	}

	data.FirstName = types.StringValue(user.FirstName)
	data.LastName = types.StringValue(user.LastName)
	data.Role = types.StringValue(user.Role)
	data.IsExternal = types.BoolValue(user.IsExternal)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
