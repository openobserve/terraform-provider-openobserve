package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &GroupResource{}
	_ resource.ResourceWithConfigure   = &GroupResource{}
	_ resource.ResourceWithImportState = &GroupResource{}
)

// NewGroupResource returns a factory for the openobserve_group resource.
func NewGroupResource() resource.Resource {
	return &GroupResource{}
}

// GroupResource manages a user group and its role assignments.
type GroupResource struct {
	client *Client
}

// GroupResourceModel holds the Terraform state for a group.
type GroupResourceModel struct {
	ID    types.String `tfsdk:"id"`
	OrgID types.String `tfsdk:"org_id"`
	Name  types.String `tfsdk:"name"`
	Users types.Set    `tfsdk:"users"`
	Roles types.Set    `tfsdk:"roles"`
}

func (r *GroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *GroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a user group. Groups bundle users together and grant them a set of custom roles, so " +
			"membership changes do not require touching each role. Groups are an OpenObserve Enterprise feature and " +
			"require OpenFGA to be enabled.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the group belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Group name, unique within the organization. Only letters, digits, and underscores are " +
					"allowed; OpenObserve rewrites any other character to an underscore, which would leave Terraform " +
					"tracking a name the server does not have.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						roleNamePattern,
						"must contain only letters, digits, and underscores",
					),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"users": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Emails of the group's members.",
			},
			"roles": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Custom roles (see `openobserve_role`) granted to every member of the group.",
			},
		},
	}
}

func (r *GroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *GroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	users := stringsFromSet(ctx, plan.Users, &resp.Diagnostics)
	roles := stringsFromSet(ctx, plan.Roles, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	if err := r.client.CreateGroup(ctx, org, name); err != nil && !isAlreadyExists(err) {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
		return
	}

	// The group is created empty, so members and roles are always applied by
	// the reconcile below rather than by the create call.
	if !r.reconcile(ctx, org, name, users, roles, &resp.Diagnostics) {
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(groupResourceID(org, name))
	r.refresh(ctx, org, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *GroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	group, err := r.client.GetGroup(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
		return
	}
	if group == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(groupResourceID(org, group.Name))
	state.Users = setFromStrings(ctx, group.Users, &resp.Diagnostics)
	state.Roles = setFromStrings(ctx, group.Roles, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *GroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan GroupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	users := stringsFromSet(ctx, plan.Users, &resp.Diagnostics)
	roles := stringsFromSet(ctx, plan.Roles, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	if !r.reconcile(ctx, org, name, users, roles, &resp.Diagnostics) {
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(groupResourceID(org, name))
	r.refresh(ctx, org, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *GroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GroupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteGroup(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
	}
}

// ImportState supports: terraform import openobserve_group.example default/sre
func (r *GroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/sre`.",
		)
		return
	}

	group, err := r.client.GetGroup(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Groups", err))
		return
	}
	if group == nil {
		resp.Diagnostics.AddError("Group not found", fmt.Sprintf("Group %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := GroupResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(group.Name),
		Users: setFromStrings(ctx, group.Users, &resp.Diagnostics),
		Roles: setFromStrings(ctx, group.Roles, &resp.Diagnostics),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// reconcile pushes the membership delta needed to match the plan.
func (r *GroupResource) reconcile(ctx context.Context, org, name string, users, roles []string, diags *diag.Diagnostics) bool {
	current, err := r.client.GetGroup(ctx, org, name)
	if err != nil {
		diags.AddError(enterpriseFeatureError("Groups", err))
		return false
	}
	if current == nil {
		diags.AddError("Group not found", fmt.Sprintf("Group %q no longer exists in org %q.", name, org))
		return false
	}

	addUsers, removeUsers := diffStrings(current.Users, users)
	addRoles, removeRoles := diffStrings(current.Roles, roles)
	if len(addUsers)+len(removeUsers)+len(addRoles)+len(removeRoles) == 0 {
		return true
	}

	update := UpdateGroupAPI{
		AddUsers:    addUsers,
		RemoveUsers: removeUsers,
		AddRoles:    addRoles,
		RemoveRoles: removeRoles,
	}
	if err := r.client.UpdateGroup(ctx, org, name, update); err != nil {
		diags.AddError(enterpriseFeatureError("Groups", err))
		return false
	}
	return true
}

func (r *GroupResource) refresh(ctx context.Context, org, name string, model *GroupResourceModel, diags *diag.Diagnostics) {
	group, err := r.client.GetGroup(ctx, org, name)
	if err != nil {
		diags.AddError(enterpriseFeatureError("Groups", err))
		return
	}
	if group == nil {
		diags.AddError("Group not found after write", fmt.Sprintf("Group %q was not found in org %q after being written.", name, org))
		return
	}
	model.Users = setFromStrings(ctx, group.Users, diags)
	model.Roles = setFromStrings(ctx, group.Roles, diags)
}

func groupResourceID(orgID, name string) string {
	return fmt.Sprintf("%s/%s", orgID, name)
}
