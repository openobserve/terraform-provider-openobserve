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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &AlertTemplateResource{}
	_ resource.ResourceWithConfigure   = &AlertTemplateResource{}
	_ resource.ResourceWithImportState = &AlertTemplateResource{}
)

// destinationTypes are the notification channel kinds a template or destination
// can target.
var destinationTypes = []string{"http", "email", "sns"}

// NewAlertTemplateResource returns a factory for the openobserve_alert_template resource.
func NewAlertTemplateResource() resource.Resource {
	return &AlertTemplateResource{}
}

// AlertTemplateResource manages the message body used to render alert notifications.
type AlertTemplateResource struct {
	client *Client
}

// AlertTemplateResourceModel holds the Terraform state for an alert template.
type AlertTemplateResourceModel struct {
	ID           types.String `tfsdk:"id"`
	OrgID        types.String `tfsdk:"org_id"`
	Name         types.String `tfsdk:"name"`
	Body         types.String `tfsdk:"body"`
	TemplateType types.String `tfsdk:"type"`
	Title        types.String `tfsdk:"title"`
	IsDefault    types.Bool   `tfsdk:"is_default"`
}

func (r *AlertTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_template"
}

func (r *AlertTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an alert template: the message body OpenObserve renders when an alert fires.\n\n" +
			"Templates support placeholders such as `{alert_name}`, `{stream_name}`, `{org_name}`, `{alert_start_time}`, " +
			"`{alert_end_time}`, and `{alert_url}`, plus `{rows}` for the matched records.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the template belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Template name, unique within the organization.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"body": schema.StringAttribute{
				Required: true,
				Description: "Message body. For `http` templates this is the JSON payload sent to the webhook; for " +
					"`email` templates it is the email body.",
			},
			"type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("http"),
				Description: "Channel this template renders for: `http` (default), `email`, or `sns`.",
				Validators:  []validator.String{stringvalidator.OneOf(destinationTypes...)},
			},
			"title": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Email subject line. Only meaningful when `type` is `email`.",
			},
			"is_default": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "Mark this template as the organization's default. OpenObserve marks a template as the " +
					"default on its own when it is the only one in the organization, so this can come back true even " +
					"when it was not requested.",
			},
		},
	}
}

func (r *AlertTemplateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *AlertTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertTemplateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.CreateAlertTemplate(ctx, org, alertTemplateFromModel(&plan)); err != nil {
		resp.Diagnostics.AddError("Error creating alert template", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(alertTemplateResourceID(org, plan.Name.ValueString()))
	r.refresh(ctx, org, plan.Name.ValueString(), &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertTemplateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	template, err := r.client.GetAlertTemplate(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert template", err.Error())
		return
	}
	if template == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(alertTemplateResourceID(org, template.Name))
	applyAlertTemplateToModel(template, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AlertTemplateResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	if err := r.client.UpdateAlertTemplate(ctx, org, name, alertTemplateFromModel(&plan)); err != nil {
		resp.Diagnostics.AddError("Error updating alert template", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(alertTemplateResourceID(org, name))
	r.refresh(ctx, org, name, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertTemplateResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlertTemplate(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting alert template", err.Error())
	}
}

// ImportState supports: terraform import openobserve_alert_template.example default/slack
func (r *AlertTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/slack`.",
		)
		return
	}

	template, err := r.client.GetAlertTemplate(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert template during import", err.Error())
		return
	}
	if template == nil {
		resp.Diagnostics.AddError("Alert template not found", fmt.Sprintf("Template %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := AlertTemplateResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(template.Name),
	}
	applyAlertTemplateToModel(template, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// refresh reloads the template so server-decided fields such as is_default end
// up in state rather than the planned value.
func (r *AlertTemplateResource) refresh(ctx context.Context, org, name string, model *AlertTemplateResourceModel, diags *diag.Diagnostics) {
	template, err := r.client.GetAlertTemplate(ctx, org, name)
	if err != nil {
		diags.AddError("Error reading alert template after write", err.Error())
		return
	}
	if template == nil {
		diags.AddError("Alert template not found after write", fmt.Sprintf("Template %q was not found in org %q after being written.", name, org))
		return
	}
	applyAlertTemplateToModel(template, model)
}

func alertTemplateFromModel(model *AlertTemplateResourceModel) AlertTemplateAPI {
	return AlertTemplateAPI{
		Name:         model.Name.ValueString(),
		Body:         model.Body.ValueString(),
		TemplateType: model.TemplateType.ValueString(),
		Title:        model.Title.ValueString(),
		IsDefault:    optBool(model.IsDefault),
	}
}

func applyAlertTemplateToModel(template *AlertTemplateAPI, model *AlertTemplateResourceModel) {
	model.Name = types.StringValue(template.Name)
	model.Body = types.StringValue(template.Body)
	model.TemplateType = types.StringValue(template.TemplateType)
	model.Title = types.StringValue(template.Title)
	model.IsDefault = types.BoolValue(template.IsDefault != nil && *template.IsDefault)
}

func alertTemplateResourceID(orgID, name string) string {
	return fmt.Sprintf("%s/%s", orgID, name)
}
