package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &DashboardResource{}
var _ resource.ResourceWithImportState = &DashboardResource{}

// NewDashboardResource returns a factory for the openobserve_dashboard resource.
func NewDashboardResource() resource.Resource {
	return &DashboardResource{}
}

// DashboardResource manages an OpenObserve dashboard.
type DashboardResource struct {
	client *Client
}

// DashboardResourceModel holds the Terraform state for a dashboard.
type DashboardResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	DashboardID types.String `tfsdk:"dashboard_id"`
	Title       types.String `tfsdk:"title"`
	Description types.String `tfsdk:"description"`
	// definition is the full dashboard JSON payload. Storing it as a raw JSON
	// string avoids brittle schema modeling of deeply nested panel definitions.
	Definition types.String `tfsdk:"definition"`
}

func (r *DashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *DashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve dashboard. The dashboard definition is expressed as a JSON string for full fidelity with complex panel configurations.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Resource ID in the format `{org_id}/{dashboard_id}`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"org_id": schema.StringAttribute{
				Required:    true,
				Description: "Organization identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"dashboard_id": schema.StringAttribute{
				Computed:    true,
				Description: "Dashboard ID assigned by OpenObserve.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"title": schema.StringAttribute{
				Required:    true,
				Description: "Dashboard title shown in the UI.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Optional description.",
			},
			"definition": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Full dashboard JSON definition. Panels, variables, and layout are encoded here.",
			},
		},
	}
}

func (r *DashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *DashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data DashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, err := r.modelToAPI(data)
	if err != nil {
		resp.Diagnostics.AddError("Error building dashboard request", err.Error())
		return
	}

	created, err := r.client.CreateDashboard(ctx, data.OrgID.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating dashboard", err.Error())
		return
	}

	data.DashboardID = types.StringValue(created.DashboardID)
	data.ID = types.StringValue(fmt.Sprintf("%s/%s", data.OrgID.ValueString(), created.DashboardID))
	data.Title = types.StringValue(created.Title)
	data.Description = types.StringValue(created.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *DashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data DashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dashboard, err := r.client.GetDashboard(ctx, data.OrgID.ValueString(), data.DashboardID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading dashboard", err.Error())
		return
	}
	if dashboard == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Title = types.StringValue(dashboard.Title)
	data.Description = types.StringValue(dashboard.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *DashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data DashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq, err := r.modelToAPI(data)
	if err != nil {
		resp.Diagnostics.AddError("Error building dashboard request", err.Error())
		return
	}

	updated, err := r.client.UpdateDashboard(ctx, data.OrgID.ValueString(), data.DashboardID.ValueString(), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Error updating dashboard", err.Error())
		return
	}

	data.Title = types.StringValue(updated.Title)
	data.Description = types.StringValue(updated.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *DashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data DashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteDashboard(ctx, data.OrgID.ValueString(), data.DashboardID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting dashboard", err.Error())
	}
}

// ImportState supports: terraform import openobserve_dashboard.example default/abc123
func (r *DashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be in the format {org_id}/{dashboard_id}, e.g. default/abc123",
		)
		return
	}

	data := DashboardResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		DashboardID: types.StringValue(parts[1]),
	}

	dashboard, err := r.client.GetDashboard(ctx, data.OrgID.ValueString(), data.DashboardID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading dashboard during import", err.Error())
		return
	}
	if dashboard == nil {
		resp.Diagnostics.AddError("Dashboard not found", fmt.Sprintf("Dashboard %q not found in org %q.", parts[1], parts[0]))
		return
	}

	data.Title = types.StringValue(dashboard.Title)
	data.Description = types.StringValue(dashboard.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *DashboardResource) modelToAPI(data DashboardResourceModel) (DashboardAPI, error) {
	req := DashboardAPI{
		Title:       data.Title.ValueString(),
		Description: data.Description.ValueString(),
	}
	if !data.Definition.IsNull() && !data.Definition.IsUnknown() {
		var raw any
		if err := json.Unmarshal([]byte(data.Definition.ValueString()), &raw); err != nil {
			return req, fmt.Errorf("definition is not valid JSON: %w", err)
		}
		if m, ok := raw.(map[string]any); ok {
			req.Panels = m["panels"]
			req.Variables = m["variables"]
		}
	}
	return req, nil
}
