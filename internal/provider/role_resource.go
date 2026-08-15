package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &RoleResource{}
	_ resource.ResourceWithConfigure   = &RoleResource{}
	_ resource.ResourceWithImportState = &RoleResource{}
)

// roleNamePattern mirrors the server's own normalization, which replaces every
// character outside this set with an underscore. Rejecting such a name up front
// beats silently tracking one the server never stored.
var roleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// permissionValues are the grant levels OpenObserve's authorization model accepts.
var permissionValues = []string{
	"AllowAll", "AllowGet", "AllowList", "AllowPost", "AllowPut", "AllowDelete",
}

var rolePermissionAttrTypes = map[string]attr.Type{
	"object":     types.StringType,
	"permission": types.StringType,
}

// NewRoleResource returns a factory for the openobserve_role resource.
func NewRoleResource() resource.Resource {
	return &RoleResource{}
}

// RoleResource manages a custom role and the permissions attached to it.
type RoleResource struct {
	client *Client
}

// RoleResourceModel holds the Terraform state for a role.
type RoleResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Name        types.String `tfsdk:"name"`
	Permissions types.Set    `tfsdk:"permissions"`
	Users       types.Set    `tfsdk:"users"`
}

// RolePermissionModel is one entry of the permissions set.
type RolePermissionModel struct {
	Object     types.String `tfsdk:"object"`
	Permission types.String `tfsdk:"permission"`
}

func (r *RoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func (r *RoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a custom role and the permissions granted to it. Custom roles are an OpenObserve " +
			"Enterprise feature and require OpenFGA to be enabled.\n\n" +
			"A permission binds a grant level to an object. An object is either a whole resource type (`stream`) or a " +
			"single entity of that type (`stream:my_logs`). Read the available resource types from the " +
			"`openobserve_resources` data source.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the role belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Role name, unique within the organization. Only letters, digits, and underscores are " +
					"allowed; OpenObserve rewrites any other character to an underscore, which would leave Terraform " +
					"tracking a name the server does not have. Standard role names such as `admin` are rejected.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(
						roleNamePattern,
						"must contain only letters, digits, and underscores",
					),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"permissions": schema.SetNestedAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Permissions granted to the role. Omitting this attribute leaves existing permissions untouched on create and manages none.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"object": schema.StringAttribute{
							Required: true,
							Description: "What the grant applies to: a whole resource type (`stream`) or one entity of " +
								"that type (`stream:my_logs`).\n\n" +
								"OpenObserve records a type-wide grant against the organization wildcard, " +
								"`stream:_all_{org_id}`. Writing the bare type is shorthand for exactly that, so the " +
								"organization identifier does not have to be repeated in every permission. Both " +
								"spellings are accepted and neither shows up as drift.",
						},
						"permission": schema.StringAttribute{
							Required:    true,
							Description: "Grant level: `AllowAll`, `AllowGet`, `AllowList`, `AllowPost`, `AllowPut`, or `AllowDelete`.",
							Validators:  []validator.String{stringvalidator.OneOf(permissionValues...)},
						},
					},
				},
			},
			"users": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Emails of users assigned this role directly. Members who hold it through an " +
					"`openobserve_group` are not listed, because the group holds the grant rather than the user.\n\n" +
					"~> Assign a role from one side only. Listing a user here and naming the same role in that user's " +
					"`custom_roles` writes the same grant from two resources, and each will try to remove the other's.",
			},
		},
	}
}

func (r *RoleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *RoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	if err := r.client.CreateRole(ctx, org, name); err != nil && !isAlreadyExists(err) {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}

	if !r.reconcile(ctx, org, name, plan.Permissions, plan.Users, &resp.Diagnostics) {
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(roleResourceID(org, name))
	r.refresh(ctx, org, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()

	exists, err := r.client.RoleExists(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(roleResourceID(org, name))
	r.refresh(ctx, org, name, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RoleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	if !r.reconcile(ctx, org, name, plan.Permissions, plan.Users, &resp.Diagnostics) {
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(roleResourceID(org, name))
	r.refresh(ctx, org, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RoleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RoleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteRole(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
	}
}

// ImportState supports: terraform import openobserve_role.example default/analyst
func (r *RoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/analyst`.",
		)
		return
	}

	exists, err := r.client.RoleExists(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	if !exists {
		resp.Diagnostics.AddError("Role not found", fmt.Sprintf("Role %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := RoleResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(parts[1]),
	}
	r.refresh(ctx, parts[0], parts[1], &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// reconcile pushes the permission and membership deltas needed to bring the
// server's view of the role in line with the plan.
func (r *RoleResource) reconcile(ctx context.Context, org, name string, plannedPermissions, plannedUsers types.Set, diags *diag.Diagnostics) bool {
	update := UpdateRoleAPI{}
	changed := false

	if !plannedPermissions.IsNull() && !plannedPermissions.IsUnknown() {
		current, err := r.client.GetRolePermissions(ctx, org, name)
		if err != nil {
			diags.AddError(enterpriseFeatureError("Custom roles", err))
			return false
		}
		desired := permissionsFromSet(ctx, org, plannedPermissions, diags)
		if diags.HasError() {
			return false
		}
		add, remove := diffPermissions(current, desired)
		update.Add, update.Remove = add, remove
		changed = changed || len(add) > 0 || len(remove) > 0
	}

	if !plannedUsers.IsNull() && !plannedUsers.IsUnknown() {
		current, err := r.client.GetRoleUsers(ctx, org, name)
		if err != nil {
			diags.AddError(enterpriseFeatureError("Custom roles", err))
			return false
		}
		desired := stringsFromSet(ctx, plannedUsers, diags)
		if diags.HasError() {
			return false
		}
		add, remove := diffStrings(current, desired)
		update.AddUsers, update.RemoveUsers = add, remove
		changed = changed || len(add) > 0 || len(remove) > 0
	}

	if !changed {
		return true
	}

	if err := r.client.UpdateRole(ctx, org, name, update); err != nil {
		diags.AddError(enterpriseFeatureError("Custom roles", err))
		return false
	}
	return true
}

func (r *RoleResource) refresh(ctx context.Context, org, name string, model *RoleResourceModel, diags *diag.Diagnostics) {
	permissions, err := r.client.GetRolePermissions(ctx, org, name)
	if err != nil {
		diags.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	model.Permissions = reconcilePermissions(ctx, org, model.Permissions, permissions, diags)

	users, err := r.client.GetRoleUsers(ctx, org, name)
	if err != nil {
		diags.AddError(enterpriseFeatureError("Custom roles", err))
		return
	}
	model.Users = setFromStrings(ctx, users, diags)
}

// expandPermissionObject turns a bare resource type into the object string
// OpenObserve stores.
//
// A grant over a whole resource type is recorded against the organization-wide
// wildcard entity, `stream:_all_myorg`. Writing that out by hand would mean
// repeating the organization identifier in every permission, so a bare type
// (`stream`) is accepted as shorthand and expanded here.
func expandPermissionObject(object, orgID string) string {
	if strings.Contains(object, ":") {
		return object
	}
	return fmt.Sprintf("%s:_all_%s", object, orgID)
}

// permissionsFromSet converts the Terraform set into API permission entries.
func permissionsFromSet(ctx context.Context, orgID string, set types.Set, diags *diag.Diagnostics) []EntityAuthorization {
	var models []RolePermissionModel
	diags.Append(set.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil
	}
	out := make([]EntityAuthorization, 0, len(models))
	for _, m := range models {
		out = append(out, EntityAuthorization{
			Object:     expandPermissionObject(m.Object.ValueString(), orgID),
			Permission: m.Permission.ValueString(),
		})
	}
	return out
}

// reconcilePermissions decides what the permissions attribute should hold after
// a write or a read.
//
// The shorthand form (`stream`) and the expanded form (`stream:_all_myorg`)
// name the same grant, and the server only ever reports the expanded one.
// Whichever spelling was configured is kept when the two describe the same set,
// so the shorthand does not read back as a permanent diff.
func reconcilePermissions(ctx context.Context, orgID string, configured types.Set, server []EntityAuthorization, diags *diag.Diagnostics) types.Set {
	serverSet := permissionsToSet(server, diags)
	if configured.IsNull() || configured.IsUnknown() {
		return serverSet
	}

	want := permissionsFromSet(ctx, orgID, configured, diags)
	if diags.HasError() {
		return serverSet
	}

	key := func(p EntityAuthorization) string { return p.Object + "\x00" + p.Permission }
	have := make(map[string]struct{}, len(server))
	for _, p := range server {
		have[key(p)] = struct{}{}
	}
	if len(want) != len(server) {
		return serverSet
	}
	for _, p := range want {
		if _, ok := have[key(p)]; !ok {
			return serverSet
		}
	}
	return configured
}

// permissionsToSet converts API permission entries into a Terraform set.
func permissionsToSet(in []EntityAuthorization, diags *diag.Diagnostics) types.Set {
	values := make([]attr.Value, 0, len(in))
	for _, p := range in {
		values = append(values, objectValue(rolePermissionAttrTypes, map[string]attr.Value{
			"object":     types.StringValue(p.Object),
			"permission": types.StringValue(p.Permission),
		}, diags))
	}
	set, d := types.SetValue(types.ObjectType{AttrTypes: rolePermissionAttrTypes}, values)
	diags.Append(d...)
	return set
}

// diffPermissions computes the permission entries to add and remove.
func diffPermissions(current, desired []EntityAuthorization) (add, remove []EntityAuthorization) {
	key := func(p EntityAuthorization) string { return p.Object + "\x00" + p.Permission }

	currentSet := make(map[string]EntityAuthorization, len(current))
	for _, p := range current {
		currentSet[key(p)] = p
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, p := range desired {
		desiredSet[key(p)] = struct{}{}
	}

	for _, p := range desired {
		if _, ok := currentSet[key(p)]; !ok {
			add = append(add, p)
		}
	}
	for k, p := range currentSet {
		if _, ok := desiredSet[k]; !ok {
			remove = append(remove, p)
		}
	}

	sortPermissions(add)
	sortPermissions(remove)
	return add, remove
}

func sortPermissions(in []EntityAuthorization) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Object != in[j].Object {
			return in[i].Object < in[j].Object
		}
		return in[i].Permission < in[j].Permission
	})
}

func roleResourceID(orgID, name string) string {
	return fmt.Sprintf("%s/%s", orgID, name)
}
