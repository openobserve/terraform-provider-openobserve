package provider

import (
	"context"
	"encoding/json"
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
	_ resource.Resource                = &DashboardResource{}
	_ resource.ResourceWithConfigure   = &DashboardResource{}
	_ resource.ResourceWithImportState = &DashboardResource{}
)

// NewDashboardResource returns a factory for the openobserve_dashboard resource.
func NewDashboardResource() resource.Resource {
	return &DashboardResource{}
}

// DashboardResource manages a dashboard document.
type DashboardResource struct {
	client *Client
}

// DashboardResourceModel holds the Terraform state for a dashboard.
type DashboardResourceModel struct {
	ID            types.String `tfsdk:"id"`
	OrgID         types.String `tfsdk:"org_id"`
	FolderID      types.String `tfsdk:"folder_id"`
	DashboardID   types.String `tfsdk:"dashboard_id"`
	DashboardJSON types.String `tfsdk:"dashboard_json"`
	Title         types.String `tfsdk:"title"`
	Description   types.String `tfsdk:"description"`
	Owner         types.String `tfsdk:"owner"`
	Role          types.String `tfsdk:"role"`
	Version       types.Int64  `tfsdk:"version"`
	Hash          types.String `tfsdk:"hash"`
	UpdatedAt     types.Int64  `tfsdk:"updated_at"`
}

func (r *DashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *DashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve dashboard.\n\n" +
			"The dashboard body is supplied as a JSON document in `dashboard_json`, the same payload the OpenObserve UI " +
			"exports. This keeps the resource faithful to every dashboard schema version rather than modelling one of " +
			"them in Terraform. Build it with `jsonencode()` or load an exported file with `file()`.\n\n" +
			"The `dashboardId` field inside the document is managed by the server: leave it out and it is filled in on " +
			"create, then preserved on every update.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{dashboard_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the dashboard belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("default"),
				Description: "Folder that holds the dashboard. Use `openobserve_folder` to create one. Changing this moves the dashboard.",
			},
			"dashboard_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned dashboard identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"dashboard_json": schema.StringAttribute{
				Required: true,
				Description: "The dashboard document as JSON, including `title`, `version`, `panels`, and any variables. " +
					"Stored normalized, so formatting differences do not show up as drift.",
			},
			"title": schema.StringAttribute{
				Computed:    true,
				Description: "Dashboard title, read from `dashboard_json`.",
			},
			"description": schema.StringAttribute{
				Computed:    true,
				Description: "Dashboard description, read from `dashboard_json`.",
			},
			"owner": schema.StringAttribute{
				Computed:    true,
				Description: "Dashboard owner recorded by the server.",
			},
			"role": schema.StringAttribute{
				Computed:    true,
				Description: "Role associated with the dashboard.",
			},
			"version": schema.Int64Attribute{
				Computed:    true,
				Description: "Dashboard schema version the server stored the document under.",
			},
			"hash": schema.StringAttribute{
				Computed:    true,
				Description: "Concurrency token. The provider sends it on update so a conflicting edit is rejected rather than silently overwritten.",
			},
			"updated_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Timestamp of the last modification, in microseconds.",
			},
		},
	}
}

func (r *DashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *DashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := rawJSONFromString(plan.DashboardJSON, "dashboard_json", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderID := plan.FolderID.ValueString()

	created, err := r.client.CreateDashboard(ctx, org, folderID, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating dashboard", err.Error())
		return
	}

	dashboardID := created.DashboardID()
	if dashboardID == "" {
		resp.Diagnostics.AddError(
			"Error resolving dashboard after create",
			"The dashboard was created but the response did not carry a dashboardId.",
		)
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.DashboardID = types.StringValue(dashboardID)
	plan.ID = types.StringValue(dashboardResourceID(org, dashboardID))
	r.applyDashboardToModel(created, &plan, plan.DashboardJSON, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	dashboardID := state.DashboardID.ValueString()

	dashboard, err := r.client.GetDashboard(ctx, org, dashboardID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading dashboard", err.Error())
		return
	}
	if dashboard == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(dashboardResourceID(org, dashboardID))
	r.applyDashboardToModel(dashboard, &state, state.DashboardJSON, &resp.Diagnostics)
	r.refreshFolder(ctx, org, dashboardID, &state)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := rawJSONFromString(plan.DashboardJSON, "dashboard_json", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	dashboardID := state.DashboardID.ValueString()
	currentFolder := state.FolderID.ValueString()
	targetFolder := plan.FolderID.ValueString()

	// The document must keep its server-assigned ID or the update creates a
	// second dashboard instead of replacing this one.
	body, err := withDashboardID(body, dashboardID)
	if err != nil {
		resp.Diagnostics.AddError("Error preparing dashboard document", err.Error())
		return
	}

	// The hash is a concurrency token from the last read, so it has to be
	// refreshed before the write rather than taken from stale state.
	hash := state.Hash.ValueString()
	if latest, readErr := r.client.GetDashboard(ctx, org, dashboardID); readErr == nil && latest != nil {
		hash = latest.Hash
	}

	if targetFolder != currentFolder && currentFolder != "" {
		if err := r.client.MoveDashboard(ctx, org, dashboardID, currentFolder, targetFolder); err != nil {
			resp.Diagnostics.AddError("Error moving dashboard between folders", err.Error())
			return
		}
	}

	updated, err := r.client.UpdateDashboard(ctx, org, dashboardID, targetFolder, hash, body)
	if err != nil {
		resp.Diagnostics.AddError("Error updating dashboard", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.DashboardID = types.StringValue(dashboardID)
	plan.ID = types.StringValue(dashboardResourceID(org, dashboardID))
	r.applyDashboardToModel(updated, &plan, plan.DashboardJSON, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDashboard(ctx, org, state.DashboardID.ValueString(), state.FolderID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting dashboard", err.Error())
	}
}

// ImportState supports: terraform import openobserve_dashboard.example default/7123abc
func (r *DashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{dashboard_id}`, for example `default/7123abc`.",
		)
		return
	}

	dashboard, err := r.client.GetDashboard(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading dashboard during import", err.Error())
		return
	}
	if dashboard == nil {
		resp.Diagnostics.AddError("Dashboard not found", fmt.Sprintf("Dashboard %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := DashboardResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		DashboardID: types.StringValue(parts[1]),
		FolderID:    types.StringValue("default"),
	}
	r.applyDashboardToModel(dashboard, &state, types.StringNull(), &resp.Diagnostics)
	r.refreshFolder(ctx, parts[0], parts[1], &state)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// refreshFolder resolves which folder holds the dashboard. The get endpoint
// does not report it, so every folder is searched.
func (r *DashboardResource) refreshFolder(ctx context.Context, org, dashboardID string, model *DashboardResourceModel) {
	folderID, err := r.client.FindDashboardFolder(ctx, org, dashboardID)
	if err != nil || folderID == "" {
		return
	}
	model.FolderID = types.StringValue(folderID)
}

func (r *DashboardResource) applyDashboardToModel(dashboard *DashboardAPI, model *DashboardResourceModel, configured types.String, diags *diag.Diagnostics) {
	model.DashboardJSON = reconcileJSON(configured, dashboard.Body(), diags)
	model.Title = types.StringValue(dashboard.Title())
	model.Description = types.StringValue(dashboard.Description())
	model.Owner = types.StringValue(dashboard.Owner())
	model.Role = types.StringValue(dashboard.Role())
	model.Version = types.Int64Value(int64(dashboard.Version))
	model.Hash = types.StringValue(dashboard.Hash)
	model.UpdatedAt = types.Int64Value(dashboard.UpdatedAt)

	if id := dashboard.DashboardID(); id != "" {
		model.DashboardID = types.StringValue(id)
	}
}

// withDashboardID sets `dashboardId` in a dashboard document, leaving the rest
// of it untouched.
func withDashboardID(body json.RawMessage, dashboardID string) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("dashboard_json must be a JSON object: %w", err)
	}
	doc["dashboardId"] = dashboardID
	return json.Marshal(doc)
}

func dashboardResourceID(orgID, dashboardID string) string {
	return fmt.Sprintf("%s/%s", orgID, dashboardID)
}
