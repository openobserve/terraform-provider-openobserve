package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &OrganizationResource{}
	_ resource.ResourceWithConfigure   = &OrganizationResource{}
	_ resource.ResourceWithImportState = &OrganizationResource{}
)

// NewOrganizationResource returns a factory for the openobserve_organization resource.
func NewOrganizationResource() resource.Resource {
	return &OrganizationResource{}
}

// OrganizationResource manages an OpenObserve organization.
type OrganizationResource struct {
	client *Client
}

// OrganizationResourceModel holds the Terraform state for an organization.
type OrganizationResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Identifier types.String `tfsdk:"identifier"`
	Name       types.String `tfsdk:"name"`
	OrgType    types.String `tfsdk:"org_type"`
	UserEmail  types.String `tfsdk:"user_email"`
}

func (r *OrganizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (r *OrganizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve organization.\n\n" +
			"~> **Note** OpenObserve has no API for deleting an organization. Destroying this resource removes it from " +
			"Terraform state and leaves the organization in place; delete it from the OpenObserve UI if that is what you want.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Organization identifier, same as `identifier`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"identifier": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Immutable organization identifier used in API paths. Derived from `name` when omitted.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name. Only letters, digits, spaces, and underscores are accepted.",
			},
			"org_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization type reported by the server, for example `default` or `custom`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"user_email": schema.StringAttribute{
				Computed:    true,
				Description: "Email of the user who owns the organization.",
			},
		},
	}
}

func (r *OrganizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *OrganizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan OrganizationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateOrganization(ctx, CreateOrgAPI{
		Identifier: plan.Identifier.ValueString(),
		Name:       plan.Name.ValueString(),
		OrgType:    plan.OrgType.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating organization", err.Error())
		return
	}

	identifier := created.Identifier
	if identifier == "" {
		identifier = plan.Identifier.ValueString()
	}
	plan.ID = types.StringValue(identifier)
	plan.Identifier = types.StringValue(identifier)
	if created.OrgType != "" {
		plan.OrgType = types.StringValue(created.OrgType)
	}

	r.readInto(ctx, identifier, &plan)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state OrganizationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org, err := r.client.GetOrganization(ctx, state.Identifier.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}
	if org == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(org.Identifier)
	state.Identifier = types.StringValue(org.Identifier)
	state.Name = types.StringValue(org.Name)
	state.OrgType = types.StringValue(org.OrgType)
	state.UserEmail = types.StringValue(org.UserEmail)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state OrganizationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	identifier := state.Identifier.ValueString()
	if plan.Name.ValueString() != state.Name.ValueString() {
		if err := r.client.RenameOrganization(ctx, identifier, plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error renaming organization", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(identifier)
	plan.Identifier = types.StringValue(identifier)
	r.readInto(ctx, identifier, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the organization from state. OpenObserve exposes no
// organization delete endpoint, so nothing is called server-side.
func (r *OrganizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state OrganizationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.AddWarning(
		"Organization not deleted",
		"OpenObserve does not expose an API for deleting organizations. "+
			state.Identifier.ValueString()+" has been removed from Terraform state but still exists on the server.",
	)
}

// ImportState supports: terraform import openobserve_organization.example my-org
func (r *OrganizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, err := r.client.GetOrganization(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization during import", err.Error())
		return
	}
	if org == nil {
		resp.Diagnostics.AddError("Organization not found", "No organization with identifier "+req.ID+" exists.")
		return
	}

	state := OrganizationResourceModel{
		ID:         types.StringValue(org.Identifier),
		Identifier: types.StringValue(org.Identifier),
		Name:       types.StringValue(org.Name),
		OrgType:    types.StringValue(org.OrgType),
		UserEmail:  types.StringValue(org.UserEmail),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readInto refreshes the computed attributes of an organization after a write.
func (r *OrganizationResource) readInto(ctx context.Context, identifier string, model *OrganizationResourceModel) {
	org, err := r.client.GetOrganization(ctx, identifier)
	if err != nil || org == nil {
		// The list endpoint can lag behind a create on a multi-node cluster.
		// Fall back to the planned values rather than failing the apply.
		if model.OrgType.IsUnknown() {
			model.OrgType = types.StringValue("")
		}
		if model.UserEmail.IsUnknown() {
			model.UserEmail = types.StringValue("")
		}
		return
	}
	model.Name = types.StringValue(org.Name)
	model.OrgType = types.StringValue(org.OrgType)
	model.UserEmail = types.StringValue(org.UserEmail)
}
