package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &FolderResource{}
	_ resource.ResourceWithConfigure   = &FolderResource{}
	_ resource.ResourceWithImportState = &FolderResource{}
)

// folderTypes are the folder namespaces OpenObserve supports.
var folderTypes = []string{"dashboards", "alerts", "reports", "synthetics"}

// NewFolderResource returns a factory for the openobserve_folder resource.
func NewFolderResource() resource.Resource {
	return &FolderResource{}
}

// FolderResource manages a dashboard or alert folder.
type FolderResource struct {
	client *Client
}

// FolderResourceModel holds the Terraform state for a folder.
type FolderResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	FolderID    types.String `tfsdk:"folder_id"`
	FolderType  types.String `tfsdk:"folder_type"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (r *FolderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_folder"
}

func (r *FolderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a folder that groups dashboards, alerts, reports, or synthetics.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{folder_type}/{folder_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization identifier. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned folder identifier. Reference this from dashboards and alerts.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"folder_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("dashboards"),
				Description: "What the folder holds: `dashboards` (default), `alerts`, `reports`, or `synthetics`.",
				Validators:  []validator.String{stringvalidator.OneOf(folderTypes...)},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Folder name, unique within the organization and folder type.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Free-form folder description.",
			},
		},
	}
}

func (r *FolderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *FolderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FolderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderType := plan.FolderType.ValueString()

	created, err := r.client.CreateFolder(ctx, org, folderType, FolderAPI{
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating folder", err.Error())
		return
	}

	folderID := created.FolderID
	if folderID == "" {
		// Older builds answer the create call with an empty body; fall back to
		// resolving the folder we just created by name.
		found, lookupErr := r.client.GetFolderByName(ctx, org, folderType, plan.Name.ValueString())
		if lookupErr != nil || found == nil {
			resp.Diagnostics.AddError(
				"Error resolving folder after create",
				"The folder was created but the server did not return its ID, and looking it up by name failed.",
			)
			return
		}
		folderID = found.FolderID
	}

	plan.OrgID = types.StringValue(org)
	plan.FolderID = types.StringValue(folderID)
	plan.ID = types.StringValue(folderResourceID(org, folderType, folderID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FolderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FolderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderType := state.FolderType.ValueString()

	folder, err := r.client.GetFolder(ctx, org, folderType, state.FolderID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading folder", err.Error())
		return
	}
	if folder == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.Name = types.StringValue(folder.Name)
	state.Description = types.StringValue(folder.Description)
	state.ID = types.StringValue(folderResourceID(org, folderType, state.FolderID.ValueString()))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FolderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state FolderResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderType := plan.FolderType.ValueString()
	folderID := state.FolderID.ValueString()

	err := r.client.UpdateFolder(ctx, org, folderType, folderID, FolderAPI{
		FolderID:    folderID,
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error updating folder", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.FolderID = types.StringValue(folderID)
	plan.ID = types.StringValue(folderResourceID(org, folderType, folderID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *FolderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FolderResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteFolder(ctx, org, state.FolderType.ValueString(), state.FolderID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting folder", err.Error())
	}
}

// ImportState supports: terraform import openobserve_folder.example default/dashboards/7123abc
func (r *FolderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{folder_type}/{folder_id}`, for example `default/dashboards/7123abc`.",
		)
		return
	}

	folder, err := r.client.GetFolder(ctx, parts[0], parts[1], parts[2])
	if err != nil {
		resp.Diagnostics.AddError("Error reading folder during import", err.Error())
		return
	}
	if folder == nil {
		resp.Diagnostics.AddError("Folder not found", fmt.Sprintf("No %s folder with ID %q exists in org %q.", parts[1], parts[2], parts[0]))
		return
	}

	state := FolderResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		FolderType:  types.StringValue(parts[1]),
		FolderID:    types.StringValue(parts[2]),
		Name:        types.StringValue(folder.Name),
		Description: types.StringValue(folder.Description),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func folderResourceID(orgID, folderType, folderID string) string {
	return fmt.Sprintf("%s/%s/%s", orgID, folderType, folderID)
}
