package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &CompositeAlertResource{}
	_ resource.ResourceWithConfigure      = &CompositeAlertResource{}
	_ resource.ResourceWithImportState    = &CompositeAlertResource{}
	_ resource.ResourceWithValidateConfig = &CompositeAlertResource{}
)

// NewCompositeAlertResource returns a factory for the openobserve_composite_alert resource.
func NewCompositeAlertResource() resource.Resource {
	return &CompositeAlertResource{}
}

// CompositeAlertResource manages an alert that combines the states of other alerts.
type CompositeAlertResource struct {
	client *Client
}

// CompositeAlertResourceModel holds the Terraform state for a composite alert.
type CompositeAlertResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	AlertID               types.String `tfsdk:"alert_id"`
	OrgID                 types.String `tfsdk:"org_id"`
	FolderID              types.String `tfsdk:"folder_id"`
	Name                  types.String `tfsdk:"name"`
	Expression            types.String `tfsdk:"expression"`
	WarningCountsAsFiring types.Bool   `tfsdk:"warning_counts_as_firing"`
	StaleChildPolicy      types.String `tfsdk:"stale_child_policy"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	Description           types.String `tfsdk:"description"`
	Destinations          types.Set    `tfsdk:"destinations"`
	Template              types.String `tfsdk:"template"`
	ContextAttributes     types.Map    `tfsdk:"context_attributes"`
	Silence               types.Int64  `tfsdk:"silence"`
	CreatesIncident       types.Bool   `tfsdk:"creates_incident"`
	Workflows             types.Set    `tfsdk:"workflows"`
	Priority              types.Int64  `tfsdk:"priority"`
	Tags                  types.Set    `tfsdk:"tags"`
	ChildAlertIDs         types.List   `tfsdk:"child_alert_ids"`
	SchedulerJobPresent   types.Bool   `tfsdk:"scheduler_job_present"`
}

func (r *CompositeAlertResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_composite_alert"
}

func (r *CompositeAlertResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve composite alert.\n\n" +
			"A composite alert has no query of its own. It combines the current states of other alerts through a " +
			"boolean `expression` and fires when that expression becomes true, which makes it the way to say " +
			"\"page me when the error rate is high *and* the deploy is recent\" without duplicating either query.\n\n" +
			"Because it reads state that its children already computed, a composite never re-runs their queries and " +
			"has no schedule of its own: it is re-evaluated when a child changes state. That is why it accepts no " +
			"`period`, `frequency`, `threshold` or query attributes, and why `silence` is the only scheduling knob here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{alert_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"alert_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned alert identifier. Composites share the alert ID namespace, so this can itself appear in another composite's `expression`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the alert belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("default"),
				Description: "Alert folder that holds this composite. Unlike an ordinary alert, which creates the default " +
					"folder on demand, a composite requires the folder to already exist.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Composite alert name, unique within the folder.",
			},
			"expression": schema.StringAttribute{
				Required: true,
				Description: "Boolean expression over child alert IDs, for example " +
					"`\"{${openobserve_alert.errors.alert_id}} && {${openobserve_alert.deploy.alert_id}}\"`.\n\n" +
					"Operands are brace-wrapped alert IDs. The operators are `&&`, `||` and `!`, with `&&` binding " +
					"tighter than `||` and `!` tighter than both; use parentheses to override that. An expression must " +
					"reference between 2 and 10 distinct children, and may not name the same child twice.\n\n" +
					"The server stores a fully parenthesized form of whatever you write, and the provider compares the " +
					"two as expressions rather than as text, so your spelling is kept and equivalent parenthesization is " +
					"not drift. After `terraform import` there is no configured spelling to keep, so state holds the " +
					"server's form until the first apply reconciles it.",
			},
			"warning_counts_as_firing": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Whether a child at warning severity counts as true. Set this to `false` to build a " +
					"composite that only reacts to children at critical.",
			},
			"stale_child_policy": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("use_last_state"),
				Description: "What a child contributes once its state goes stale, meaning it has not been evaluated " +
					"within three times its own cadence.\n\n" +
					"`use_last_state` (default) keeps trusting the last value it reported. `treat_as_false` makes a " +
					"stale child stop satisfying the expression, so a broken child cannot hold a composite firing. " +
					"`treat_as_true` is the fail-safe choice for absence-of-heartbeat patterns, where a child going " +
					"quiet is itself the signal.",
				Validators: []validator.String{stringvalidator.OneOf(CompositeStalePolicies...)},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the composite is evaluated. Disabled composites stay configured but never fire.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What the composite watches for.",
			},
			"destinations": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Names of the `openobserve_alert_destination` resources notified when the composite fires.",
			},
			"template": schema.StringAttribute{
				Optional:    true,
				Description: "Template used for every destination, overriding each destination's own template.",
			},
			"context_attributes": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Extra key/value pairs made available to the notification template.",
			},
			"silence": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				Description: "Minutes the composite stays quiet after firing. This is the only scheduling attribute a " +
					"composite accepts; it has no period or frequency because it does not run a query.",
			},
			"creates_incident": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Route notifications through the incident system instead of sending them directly.",
			},
			"workflows": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "IDs of workflows triggered when the composite fires.",
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Description: "Priority from 1 (most urgent) to 5. Display and propagation only; it does not affect when the composite fires.",
			},
			"tags": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Selection tags, for example `prod` or `service:checkout`.",
			},
			"child_alert_ids": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Child alert IDs the server resolved from `expression`, in the order they appear in it.",
			},
			"scheduler_job_present": schema.BoolAttribute{
				Computed: true,
				Description: "Whether a scheduler job currently backs this composite. A disabled composite has none; " +
					"an enabled composite without one has not been picked up yet.",
			},
		},
	}
}

// ValidateConfig reports what the provider can determine without the server:
// syntax, operand count, and duplicate operands. Whether the children exist,
// are eligible, or would close a cycle needs the organization's alert graph,
// so those stay server-side checks.
func (r *CompositeAlertResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model CompositeAlertResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An expression built from resource references is unknown until those
	// resources exist, which is the normal case on a first apply.
	if model.Expression.IsNull() || model.Expression.IsUnknown() {
		return
	}
	if _, _, err := validateCompositeExpression(model.Expression.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			path.Root("expression"),
			"Invalid composite expression",
			err.Error(),
		)
	}
}

func (r *CompositeAlertResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *CompositeAlertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CompositeAlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	folderID := plan.FolderID.ValueString()
	body := r.bodyFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	alertID, err := r.client.CreateCompositeAlert(ctx, org, folderID, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating composite alert", compositeErrorDetail(err))
		return
	}
	if alertID == "" {
		found, lookupErr := r.client.FindAlertByName(ctx, org, folderID, plan.Name.ValueString())
		if lookupErr != nil || found == nil {
			resp.Diagnostics.AddError(
				"Error resolving composite alert after create",
				"The composite was created but the server did not return its ID, and looking it up by name failed.",
			)
			return
		}
		alertID = found.AlertID
	}

	plan.OrgID = types.StringValue(org)
	plan.AlertID = types.StringValue(alertID)
	plan.ID = types.StringValue(alertResourceID(org, alertID))

	r.refresh(ctx, org, alertID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *CompositeAlertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CompositeAlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	alertID := state.AlertID.ValueString()

	composite, err := r.client.GetCompositeAlert(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading composite alert", err.Error())
		return
	}
	if composite == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(alertResourceID(org, alertID))
	r.applyToModel(ctx, composite, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *CompositeAlertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state CompositeAlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	alertID := state.AlertID.ValueString()
	folderID := plan.FolderID.ValueString()
	body := r.bodyFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateCompositeAlert(ctx, org, alertID, body); err != nil {
		resp.Diagnostics.AddError("Error updating composite alert", compositeErrorDetail(err))
		return
	}

	// As with ordinary alerts, an update never changes the folder, so a folder
	// change is a separate move.
	if folderID != "" && folderID != state.FolderID.ValueString() {
		if err := r.client.MoveAlerts(ctx, org, folderID, []string{alertID}); err != nil {
			resp.Diagnostics.AddError("Error moving composite alert between folders", err.Error())
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.AlertID = types.StringValue(alertID)
	plan.ID = types.StringValue(alertResourceID(org, alertID))

	r.refresh(ctx, org, alertID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *CompositeAlertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CompositeAlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlert(ctx, org, state.AlertID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting composite alert", compositeErrorDetail(err))
	}
}

// ImportState supports: terraform import openobserve_composite_alert.example default/2abcXYZ
func (r *CompositeAlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{alert_id}`, for example `default/2abcXYZ`.",
		)
		return
	}

	composite, err := r.client.GetCompositeAlert(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading composite alert during import", err.Error())
		return
	}
	if composite == nil {
		resp.Diagnostics.AddError(
			"Composite alert not found",
			fmt.Sprintf("Composite alert %q not found in org %q.", parts[1], parts[0]),
		)
		return
	}

	state := CompositeAlertResourceModel{
		ID:      types.StringValue(req.ID),
		OrgID:   types.StringValue(parts[0]),
		AlertID: types.StringValue(parts[1]),
	}
	r.applyToModel(ctx, composite, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------------------
// Model <-> API conversion
// ---------------------------------------------------------------------------

func (r *CompositeAlertResource) bodyFromModel(ctx context.Context, model *CompositeAlertResourceModel, diags *diag.Diagnostics) CompositeAlertWriteAPI {
	destinations := stringsFromSet(ctx, model.Destinations, diags)
	if destinations == nil {
		destinations = []string{}
	}

	return CompositeAlertWriteAPI{
		AlertType: "composite",
		CompositeCondition: CompositeConditionAPI{
			Expression:            model.Expression.ValueString(),
			WarningCountsAsFiring: model.WarningCountsAsFiring.ValueBool(),
			StaleChildPolicy:      model.StaleChildPolicy.ValueString(),
		},
		Name:             model.Name.ValueString(),
		Description:      model.Description.ValueString(),
		Enabled:          model.Enabled.ValueBool(),
		Destinations:     destinations,
		Template:         optString(model.Template),
		ContextAttrs:     stringsFromMap(ctx, model.ContextAttributes, diags),
		TriggerCondition: CompositeTriggerAPI{Silence: model.Silence.ValueInt64()},
		CreatesIncident:  model.CreatesIncident.ValueBool(),
		Workflows:        stringsFromSet(ctx, model.Workflows, diags),
		Priority:         optInt64(model.Priority),
		Tags:             stringsFromSet(ctx, model.Tags, diags),
	}
}

func (r *CompositeAlertResource) refresh(ctx context.Context, org, alertID string, model *CompositeAlertResourceModel, diags *diag.Diagnostics) {
	composite, err := r.client.GetCompositeAlert(ctx, org, alertID)
	if err != nil {
		diags.AddError("Error reading composite alert after write", err.Error())
		return
	}
	if composite == nil {
		diags.AddError(
			"Composite alert not found after write",
			fmt.Sprintf("Composite alert %q was not found in org %q after being written.", alertID, org),
		)
		return
	}
	r.applyToModel(ctx, composite, model, diags)
}

func (r *CompositeAlertResource) applyToModel(ctx context.Context, api *CompositeAlertAPI, model *CompositeAlertResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(api.Name)
	model.Enabled = types.BoolValue(api.Enabled)
	model.CreatesIncident = types.BoolValue(api.CreatesIncident)
	model.Silence = types.Int64Value(api.TriggerCondition.Silence)
	model.Template = stringFromPtr(api.Template)
	model.Priority = int64FromPtr(api.Priority)
	model.SchedulerJobPresent = boolFromPtr(api.SchedulerJobPresent)

	if api.Description != nil {
		model.Description = types.StringValue(*api.Description)
	} else {
		model.Description = types.StringValue("")
	}
	if api.FolderID != "" {
		model.FolderID = types.StringValue(api.FolderID)
	}

	// The server persists a fully parenthesized rewrite of the expression, so
	// keep the spelling already in the model whenever it means the same thing.
	// Without this every plan would report a change on a configuration nobody
	// touched.
	//
	// A plan modifier cannot do this instead: `expression` is Required, and
	// overriding a Required attribute's configured value in the plan leaves
	// Terraform proposing the same update forever.
	stored := api.CompositeCondition.Expression
	if !compositeExpressionsEquivalent(model.Expression.ValueString(), stored) {
		model.Expression = types.StringValue(stored)
	}
	model.WarningCountsAsFiring = types.BoolValue(api.CompositeCondition.WarningCountsAsFiring)
	if api.CompositeCondition.StaleChildPolicy != "" {
		model.StaleChildPolicy = types.StringValue(api.CompositeCondition.StaleChildPolicy)
	}

	model.Destinations = setFromStrings(ctx, api.Destinations, diags)
	model.ContextAttributes = mapFromStrings(ctx, api.ContextAttrs, diags)
	if len(api.Workflows) == 0 {
		model.Workflows = types.SetNull(types.StringType)
	} else {
		model.Workflows = setFromStrings(ctx, api.Workflows, diags)
	}
	if len(api.Tags) == 0 {
		model.Tags = types.SetNull(types.StringType)
	} else {
		model.Tags = setFromStrings(ctx, api.Tags, diags)
	}

	childIDs := make([]string, 0, len(api.Children))
	for _, child := range api.Children {
		childIDs = append(childIDs, child.AlertID)
	}
	model.ChildAlertIDs = listFromStrings(ctx, childIDs, diags)
}

// compositeErrorDetail annotates the server's machine-readable composite error
// codes, which are precise but assume knowledge of the composite graph.
func compositeErrorDetail(err error) string {
	msg := err.Error()
	hint := ""
	switch {
	case strings.Contains(msg, "child_referenced"):
		hint = "\n\nA composite still references this alert as a child. Destroy the composite first, or remove the " +
			"child from its expression; Terraform cannot infer that ordering unless the composite's `expression` " +
			"references the child resource's `alert_id`, which is also what makes the dependency explicit."
	case strings.Contains(msg, "child_not_eligible"):
		hint = "\n\nReal-time alerts cannot be composite children: they have no scheduled state for a composite to read."
	case strings.Contains(msg, "composite_cycle"):
		hint = "\n\nComposites may reference other composites, but the references must form a tree."
	case strings.Contains(msg, "composite_too_deep"):
		hint = "\n\nA composite may reference another composite, but not one that itself references a third."
	case strings.Contains(msg, "composite_writes_disabled"):
		hint = "\n\nComposite mutation is disabled on this server. It is enabled by default; check whether " +
			"ZO_ALERT_COMPOSITE_WRITES_ENABLED has been set to false."
	case strings.Contains(msg, "composite_super_cluster_unsupported"):
		hint = "\n\nComposite alerts are not available on super-cluster deployments."
	case strings.Contains(msg, "composite_folder_not_found"):
		hint = "\n\nUnlike an ordinary alert, a composite does not create its folder on demand. Create the " +
			"`openobserve_folder` first and reference its `folder_id`."
	case strings.Contains(msg, "composite_graph_lock_unavailable"):
		hint = "\n\nAnother composite write is in flight for this organization. Composite writes serialize on a " +
			"per-organization lock, so retry, or apply with -parallelism=1 when creating several at once."
	}
	return msg + hint
}
