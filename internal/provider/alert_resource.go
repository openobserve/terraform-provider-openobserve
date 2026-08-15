package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	_ resource.Resource                   = &AlertResource{}
	_ resource.ResourceWithConfigure      = &AlertResource{}
	_ resource.ResourceWithImportState    = &AlertResource{}
	_ resource.ResourceWithValidateConfig = &AlertResource{}
)

// comparisonOperators are the operators accepted in alert thresholds and conditions.
var comparisonOperators = []string{"=", "!=", ">", ">=", "<", "<=", "Contains", "NotContains"}

// aggregationFunctions are the aggregate functions a `custom` alert can apply.
var aggregationFunctions = []string{"avg", "min", "max", "sum", "count", "median", "p50", "p75", "p90", "p95", "p99"}

// NewAlertResource returns a factory for the openobserve_alert resource.
func NewAlertResource() resource.Resource {
	return &AlertResource{}
}

// AlertResource manages a scheduled or real-time alert.
type AlertResource struct {
	client *Client
}

// AlertResourceModel holds the Terraform state for an alert.
type AlertResourceModel struct {
	ID                types.String                `tfsdk:"id"`
	AlertID           types.String                `tfsdk:"alert_id"`
	OrgID             types.String                `tfsdk:"org_id"`
	FolderID          types.String                `tfsdk:"folder_id"`
	Name              types.String                `tfsdk:"name"`
	StreamType        types.String                `tfsdk:"stream_type"`
	StreamName        types.String                `tfsdk:"stream_name"`
	IsRealTime        types.Bool                  `tfsdk:"is_real_time"`
	Enabled           types.Bool                  `tfsdk:"enabled"`
	Description       types.String                `tfsdk:"description"`
	Destinations      types.Set                   `tfsdk:"destinations"`
	Template          types.String                `tfsdk:"template"`
	ContextAttributes types.Map                   `tfsdk:"context_attributes"`
	RowTemplate       types.String                `tfsdk:"row_template"`
	TZOffset          types.Int64                 `tfsdk:"tz_offset"`
	Owner             types.String                `tfsdk:"owner"`
	Priority          types.Int64                 `tfsdk:"priority"`
	Tags              types.Set                   `tfsdk:"tags"`
	QueryCondition    *AlertQueryConditionModel   `tfsdk:"query_condition"`
	TriggerCondition  *AlertTriggerConditionModel `tfsdk:"trigger_condition"`
	LastTriggeredAt   types.Int64                 `tfsdk:"last_triggered_at"`
	LastSatisfiedAt   types.Int64                 `tfsdk:"last_satisfied_at"`
	UpdatedAt         types.Int64                 `tfsdk:"updated_at"`
	LastEditedBy      types.String                `tfsdk:"last_edited_by"`
}

// AlertQueryConditionModel is the query_condition block.
type AlertQueryConditionModel struct {
	QueryType       types.String           `tfsdk:"type"`
	SQL             types.String           `tfsdk:"sql"`
	PromQL          types.String           `tfsdk:"promql"`
	PromQLCondition *AlertConditionModel   `tfsdk:"promql_condition"`
	Conditions      types.String           `tfsdk:"conditions"`
	Aggregation     *AlertAggregationModel `tfsdk:"aggregation"`
	VRLFunction     types.String           `tfsdk:"vrl_function"`
	SearchEventType types.String           `tfsdk:"search_event_type"`
	MultiTimeRange  []AlertTimeOffsetModel `tfsdk:"multi_time_range"`
}

// AlertConditionModel is a single column/operator/value comparison.
type AlertConditionModel struct {
	Column   types.String `tfsdk:"column"`
	Operator types.String `tfsdk:"operator"`
	Value    types.String `tfsdk:"value"`
}

// AlertAggregationModel is the aggregation block of a `custom` alert.
type AlertAggregationModel struct {
	GroupBy      types.List           `tfsdk:"group_by"`
	Function     types.String         `tfsdk:"function"`
	Having       *AlertConditionModel `tfsdk:"having"`
	WarningValue types.Float64        `tfsdk:"warning_value"`
	MultiAlert   types.Bool           `tfsdk:"multi_alert"`
}

// AlertTimeOffsetModel is one entry of multi_time_range.
type AlertTimeOffsetModel struct {
	Offset types.String `tfsdk:"offset"`
}

// AlertTriggerConditionModel is the trigger_condition block.
type AlertTriggerConditionModel struct {
	Period           types.Int64  `tfsdk:"period"`
	Operator         types.String `tfsdk:"operator"`
	Threshold        types.Int64  `tfsdk:"threshold"`
	WarningThreshold types.Int64  `tfsdk:"warning_threshold"`
	NotifyOnWarning  types.Bool   `tfsdk:"notify_on_warning"`
	Frequency        types.Int64  `tfsdk:"frequency"`
	FrequencyType    types.String `tfsdk:"frequency_type"`
	Cron             types.String `tfsdk:"cron"`
	Silence          types.Int64  `tfsdk:"silence"`
	Timezone         types.String `tfsdk:"timezone"`
	ToleranceInSecs  types.Int64  `tfsdk:"tolerance_in_secs"`
	AlignTime        types.Bool   `tfsdk:"align_time"`
}

func (r *AlertResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert"
}

func (r *AlertResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	conditionAttributes := func(required bool) map[string]schema.Attribute {
		return map[string]schema.Attribute{
			"column": schema.StringAttribute{
				Required:    required,
				Optional:    !required,
				Description: "Column the comparison reads.",
			},
			"operator": schema.StringAttribute{
				Required:    required,
				Optional:    !required,
				Description: "Comparison operator: `=`, `!=`, `>`, `>=`, `<`, `<=`, `Contains`, or `NotContains`.",
				Validators:  []validator.String{stringvalidator.OneOf(comparisonOperators...)},
			},
			"value": schema.StringAttribute{
				Required: required,
				Optional: !required,
				Description: "Value to compare against. A value that parses as a number is sent as a JSON number; " +
					"anything else is sent as a JSON string.",
			},
		}
	}

	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve alert.\n\n" +
			"An alert evaluates a query against a stream and notifies one or more destinations when the result crosses " +
			"a threshold. Set `is_real_time` for alerts that fire as matching data arrives; leave it false for scheduled " +
			"alerts driven by `trigger_condition.frequency`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{alert_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"alert_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned alert identifier.",
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
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("default"),
				Description: "Alert folder that holds this alert. Use `openobserve_folder` with `folder_type = \"alerts\"` to create one.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Alert name, unique within the folder.",
			},
			"stream_type": schema.StringAttribute{
				Required:      true,
				Description:   "Stream type the alert queries: `logs`, `metrics`, or `traces`.",
				Validators:    []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"stream_name": schema.StringAttribute{
				Required:      true,
				Description:   "Stream the alert queries.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"is_real_time": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Evaluate as data arrives instead of on a schedule.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the alert is evaluated. Disabled alerts stay configured but never fire.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What the alert monitors.",
			},
			"destinations": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Names of the `openobserve_alert_destination` resources notified when the alert fires.",
			},
			"template": schema.StringAttribute{
				Optional: true,
				Description: "Template used for every destination, overriding each destination's own template. Lets one " +
					"set of destinations serve alerts with different message formats.",
			},
			"context_attributes": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Extra key/value pairs made available to the notification template.",
			},
			"row_template": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Template applied to each matched row when rendering `{rows}` in the message.",
			},
			"tz_offset": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Timezone offset in minutes, negative west of UTC.",
			},
			"owner": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Email of the alert owner. Defaults to the account the provider authenticates as.",
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Description: "Triage priority from 1 (most urgent) to 5. Display metadata only; it does not affect when the alert fires.",
			},
			"tags": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Selection tags such as `prod` or `service:checkout`.",
			},
			"last_triggered_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Timestamp of the last time the alert fired, in microseconds.",
			},
			"last_satisfied_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Timestamp of the last time the alert condition was met, in microseconds.",
			},
			"updated_at": schema.Int64Attribute{
				Computed:    true,
				Description: "Timestamp of the last modification, in microseconds.",
			},
			"last_edited_by": schema.StringAttribute{
				Computed:    true,
				Description: "Account that last modified the alert.",
			},
		},
		Blocks: map[string]schema.Block{
			"query_condition": schema.SingleNestedBlock{
				Description: "What the alert evaluates.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString("custom"),
						Description: "Query style: `custom` (UI-style conditions, default), `sql`, or `promql`.",
						Validators:  []validator.String{stringvalidator.OneOf("custom", "sql", "promql")},
					},
					"sql": schema.StringAttribute{
						Optional:    true,
						Description: "SQL query returning an aggregate to compare against the threshold. Required when `type` is `sql`.",
					},
					"promql": schema.StringAttribute{
						Optional:    true,
						Description: "PromQL expression. Required when `type` is `promql`.",
					},
					"conditions": schema.StringAttribute{
						Optional: true,
						Description: "Filter conditions for `custom` alerts, as a JSON document. Use `jsonencode()` to build it. " +
							"Both the flat and the nested (grouped) condition formats are accepted.",
					},
					"vrl_function": schema.StringAttribute{
						Optional:    true,
						Description: "VRL function applied to results before evaluation.",
					},
					"search_event_type": schema.StringAttribute{
						Optional:    true,
						Description: "Search event classification recorded for the alert's queries.",
					},
				},
				Blocks: map[string]schema.Block{
					"promql_condition": schema.SingleNestedBlock{
						Description: "Comparison applied to the PromQL result. Required when `type` is `promql`.",
						Attributes:  conditionAttributes(false),
					},
					"aggregation": schema.SingleNestedBlock{
						Description: "Aggregation applied before the threshold comparison. Only used when `type` is `custom`.",
						Attributes: map[string]schema.Attribute{
							"group_by": schema.ListAttribute{
								Optional:    true,
								ElementType: types.StringType,
								Description: "Columns to group by before aggregating.",
							},
							"function": schema.StringAttribute{
								Optional:    true,
								Description: "Aggregate function: `avg`, `min`, `max`, `sum`, `count`, `median`, or a percentile such as `p95`.",
								Validators:  []validator.String{stringvalidator.OneOf(aggregationFunctions...)},
							},
							"warning_value": schema.Float64Attribute{
								Optional: true,
								Description: "Warning-level threshold for the aggregate, sharing the operator and column of `having`. " +
									"Must be strictly less severe than the critical value.",
							},
							"multi_alert": schema.BoolAttribute{
								Optional:    true,
								Description: "Evaluate and notify per group instead of collapsing to a single result. Requires a non-empty `group_by`.",
							},
						},
						Blocks: map[string]schema.Block{
							"having": schema.SingleNestedBlock{
								Description: "Critical threshold applied to the aggregate value.",
								Attributes:  conditionAttributes(false),
							},
						},
					},
					"multi_time_range": schema.ListNestedBlock{
						Description: "Historical windows to compare the current window against.",
						NestedObject: schema.NestedBlockObject{
							Attributes: map[string]schema.Attribute{
								"offset": schema.StringAttribute{
									Required:    true,
									Description: "How far back the comparison window sits, for example `1h` or `1d`.",
								},
							},
						},
					},
				},
			},
			"trigger_condition": schema.SingleNestedBlock{
				Description: "When and how often the alert is evaluated, and what counts as firing.",
				Attributes: map[string]schema.Attribute{
					"period": schema.Int64Attribute{
						Optional:    true,
						Computed:    true,
						Default:     int64default.StaticInt64(10),
						Description: "Lookback window in minutes that each evaluation queries.",
					},
					"operator": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString(">="),
						Description: "Operator comparing the result to `threshold`.",
						Validators:  []validator.String{stringvalidator.OneOf(comparisonOperators...)},
					},
					"threshold": schema.Int64Attribute{
						Optional:    true,
						Computed:    true,
						Default:     int64default.StaticInt64(1),
						Description: "Critical threshold the result is compared against.",
					},
					"warning_threshold": schema.Int64Attribute{
						Optional: true,
						Description: "Warning threshold sharing `operator` with the critical one. Must be strictly less " +
							"severe than `threshold`; omit for a single-level alert.",
					},
					"notify_on_warning": schema.BoolAttribute{
						Optional: true,
						Description: "Whether a warning-level match sends a notification. Defaults to true; set false to " +
							"record warnings without paging.",
					},
					"frequency": schema.Int64Attribute{
						Optional:    true,
						Computed:    true,
						Default:     int64default.StaticInt64(1),
						Description: "How often the alert runs, in minutes. Used when `frequency_type` is `minutes`.",
					},
					"frequency_type": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString("minutes"),
						Description: "Scheduling style: `minutes` (default) or `cron`.",
						Validators:  []validator.String{stringvalidator.OneOf("minutes", "cron")},
					},
					"cron": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString(""),
						Description: "Cron expression. Required when `frequency_type` is `cron`.",
					},
					"silence": schema.Int64Attribute{
						Optional:    true,
						Computed:    true,
						Default:     int64default.StaticInt64(0),
						Description: "Minutes to stay quiet after firing before the alert may fire again.",
					},
					"timezone": schema.StringAttribute{
						Optional:    true,
						Description: "Timezone for the cron schedule, for example `America/New_York`.",
					},
					"tolerance_in_secs": schema.Int64Attribute{
						Optional:    true,
						Description: "Tolerance in seconds applied to the evaluation window boundaries.",
					},
					"align_time": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(true),
						Description: "Align each query window to period boundaries.",
					},
				},
			},
		},
	}
}

// ValidateConfig catches query/trigger combinations the API rejects.
func (r *AlertResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config AlertResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.QueryCondition == nil {
		return
	}

	queryType := config.QueryCondition.QueryType.ValueString()
	if config.QueryCondition.QueryType.IsNull() || config.QueryCondition.QueryType.IsUnknown() {
		queryType = "custom"
	}

	switch queryType {
	case "sql":
		if config.QueryCondition.SQL.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("query_condition").AtName("sql"),
				"Missing sql",
				"`sql` is required when `query_condition.type` is `sql`.",
			)
		}
	case "promql":
		if config.QueryCondition.PromQL.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("query_condition").AtName("promql"),
				"Missing promql",
				"`promql` is required when `query_condition.type` is `promql`.",
			)
		}
	}

	if config.TriggerCondition != nil &&
		config.TriggerCondition.FrequencyType.ValueString() == "cron" &&
		config.TriggerCondition.Cron.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("trigger_condition").AtName("cron"),
			"Missing cron",
			"`cron` is required when `trigger_condition.frequency_type` is `cron`.",
		)
	}
}

func (r *AlertResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *AlertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	folderID := plan.FolderID.ValueString()
	body := r.alertFromModel(ctx, &plan, org, folderID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	alertID, err := r.client.CreateAlert(ctx, org, folderID, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating alert", err.Error())
		return
	}
	if alertID == "" {
		// Some builds omit the ID from the create envelope; resolve it by name.
		found, lookupErr := r.client.FindAlertByName(ctx, org, folderID, plan.Name.ValueString())
		if lookupErr != nil || found == nil {
			resp.Diagnostics.AddError(
				"Error resolving alert after create",
				"The alert was created but the server did not return its ID, and looking it up by name failed.",
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

func (r *AlertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	alertID := state.AlertID.ValueString()

	alert, err := r.client.GetAlert(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert", err.Error())
		return
	}
	if alert == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(alertResourceID(org, alertID))
	r.applyAlertToModel(ctx, alert, &state, &resp.Diagnostics)
	r.refreshFolder(ctx, org, alertID, &state)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	alertID := state.AlertID.ValueString()
	folderID := plan.FolderID.ValueString()
	body := r.alertFromModel(ctx, &plan, org, folderID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body.ID = alertID

	if err := r.client.UpdateAlert(ctx, org, alertID, body); err != nil {
		resp.Diagnostics.AddError("Error updating alert", err.Error())
		return
	}

	// Updating an alert never changes its folder, so a folder change is a
	// separate move.
	if folderID != "" && folderID != state.FolderID.ValueString() {
		if err := r.client.MoveAlerts(ctx, org, folderID, []string{alertID}); err != nil {
			resp.Diagnostics.AddError("Error moving alert between folders", err.Error())
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

func (r *AlertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlert(ctx, org, state.AlertID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting alert", err.Error())
	}
}

// ImportState supports: terraform import openobserve_alert.example default/2abcXYZ
func (r *AlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{alert_id}`, for example `default/2abcXYZ`.",
		)
		return
	}

	alert, err := r.client.GetAlert(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert during import", err.Error())
		return
	}
	if alert == nil {
		resp.Diagnostics.AddError("Alert not found", fmt.Sprintf("Alert %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := AlertResourceModel{
		ID:       types.StringValue(req.ID),
		OrgID:    types.StringValue(parts[0]),
		AlertID:  types.StringValue(parts[1]),
		FolderID: types.StringValue("default"),
	}
	r.applyAlertToModel(ctx, alert, &state, &resp.Diagnostics)
	r.refreshFolder(ctx, parts[0], parts[1], &state)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------------------
// Model <-> API conversion
// ---------------------------------------------------------------------------

func (r *AlertResource) alertFromModel(ctx context.Context, model *AlertResourceModel, org, folderID string, diags *diag.Diagnostics) AlertAPI {
	destinations := stringsFromSet(ctx, model.Destinations, diags)
	if destinations == nil {
		destinations = []string{}
	}

	alert := AlertAPI{
		Name:         model.Name.ValueString(),
		OrgID:        org,
		StreamType:   model.StreamType.ValueString(),
		StreamName:   model.StreamName.ValueString(),
		IsRealTime:   model.IsRealTime.ValueBool(),
		Destinations: destinations,
		Template:     optString(model.Template),
		ContextAttrs: stringsFromMap(ctx, model.ContextAttributes, diags),
		RowTemplate:  model.RowTemplate.ValueString(),
		Description:  model.Description.ValueString(),
		Enabled:      model.Enabled.ValueBool(),
		TZOffset:     int32(model.TZOffset.ValueInt64()),
		Owner:        optString(model.Owner),
		Priority:     optInt64(model.Priority),
		Tags:         stringsFromSet(ctx, model.Tags, diags),
	}
	if folderID != "" {
		alert.FolderID = &folderID
	}

	if model.QueryCondition != nil {
		alert.QueryCondition = queryConditionFromModel(ctx, model.QueryCondition, diags)
	}
	if model.TriggerCondition != nil {
		alert.TriggerCondition = triggerConditionFromModel(model.TriggerCondition)
	}
	return alert
}

func queryConditionFromModel(ctx context.Context, model *AlertQueryConditionModel, diags *diag.Diagnostics) AlertQueryConditionAPI {
	out := AlertQueryConditionAPI{
		QueryType:       model.QueryType.ValueString(),
		SQL:             optString(model.SQL),
		PromQL:          optString(model.PromQL),
		VRLFunction:     optString(model.VRLFunction),
		SearchEventType: optString(model.SearchEventType),
	}
	if out.QueryType == "" {
		out.QueryType = "custom"
	}
	out.Conditions = rawJSONFromString(model.Conditions, "query_condition.conditions", diags)

	if model.PromQLCondition != nil && !model.PromQLCondition.Column.IsNull() {
		condition := conditionFromModel(model.PromQLCondition, diags)
		out.PromQLCondition = &condition
	}

	if model.Aggregation != nil && !model.Aggregation.Function.IsNull() {
		aggregation := &AlertAggregationAPI{
			GroupBy:      stringsFromList(ctx, model.Aggregation.GroupBy, diags),
			Function:     model.Aggregation.Function.ValueString(),
			WarningValue: optFloat64(model.Aggregation.WarningValue),
			MultiAlert:   model.Aggregation.MultiAlert.ValueBool(),
		}
		if model.Aggregation.Having != nil {
			aggregation.Having = conditionFromModel(model.Aggregation.Having, diags)
		}
		out.Aggregation = aggregation
	}

	for _, tr := range model.MultiTimeRange {
		out.MultiTimeRange = append(out.MultiTimeRange, AlertCompareHistoricData{Offset: tr.Offset.ValueString()})
	}
	return out
}

func conditionFromModel(model *AlertConditionModel, diags *diag.Diagnostics) AlertConditionAPI {
	return AlertConditionAPI{
		Column:   model.Column.ValueString(),
		Operator: model.Operator.ValueString(),
		Value:    encodeConditionValue(model.Value.ValueString(), diags),
	}
}

// encodeConditionValue renders a threshold as JSON. Numeric-looking input
// becomes a JSON number because that is what a threshold comparison needs;
// everything else becomes a JSON string.
func encodeConditionValue(raw string, diags *diag.Diagnostics) json.RawMessage {
	if raw == "" {
		return json.RawMessage(`""`)
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return json.RawMessage(raw)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		diags.AddError("Error encoding condition value", err.Error())
		return json.RawMessage(`""`)
	}
	return encoded
}

// decodeConditionValue renders a JSON threshold back as a plain string, so it
// round-trips through Terraform state unchanged.
func decodeConditionValue(raw json.RawMessage) types.String {
	if len(raw) == 0 {
		return types.StringNull()
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return types.StringValue(s)
	}
	return types.StringValue(strings.TrimSpace(string(raw)))
}

func triggerConditionFromModel(model *AlertTriggerConditionModel) AlertTriggerConditionAPI {
	frequencyType := model.FrequencyType.ValueString()
	if frequencyType == "" {
		frequencyType = "minutes"
	}
	operator := model.Operator.ValueString()
	if operator == "" {
		operator = ">="
	}
	return AlertTriggerConditionAPI{
		Period:           model.Period.ValueInt64(),
		Operator:         operator,
		Threshold:        model.Threshold.ValueInt64(),
		WarningThreshold: optInt64(model.WarningThreshold),
		NotifyOnWarning:  optBool(model.NotifyOnWarning),
		Frequency:        model.Frequency.ValueInt64(),
		Cron:             model.Cron.ValueString(),
		FrequencyType:    frequencyType,
		Silence:          model.Silence.ValueInt64(),
		Timezone:         optString(model.Timezone),
		ToleranceInSecs:  optInt64(model.ToleranceInSecs),
		AlignTime:        model.AlignTime.ValueBool(),
	}
}

func (r *AlertResource) refresh(ctx context.Context, org, alertID string, model *AlertResourceModel, diags *diag.Diagnostics) {
	alert, err := r.client.GetAlert(ctx, org, alertID)
	if err != nil {
		diags.AddError("Error reading alert after write", err.Error())
		return
	}
	if alert == nil {
		diags.AddError("Alert not found after write", fmt.Sprintf("Alert %q was not found in org %q after being written.", alertID, org))
		return
	}
	r.applyAlertToModel(ctx, alert, model, diags)
}

// refreshFolder resolves which folder holds the alert. The get endpoint does
// not report it, so it comes from the list endpoint.
func (r *AlertResource) refreshFolder(ctx context.Context, org, alertID string, model *AlertResourceModel) {
	alerts, err := r.client.ListAlerts(ctx, org, "")
	if err != nil {
		return
	}
	for _, a := range alerts {
		if a.AlertID == alertID {
			model.FolderID = types.StringValue(a.FolderID)
			return
		}
	}
}

func (r *AlertResource) applyAlertToModel(ctx context.Context, alert *AlertAPI, model *AlertResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(alert.Name)
	model.StreamType = types.StringValue(alert.StreamType)
	model.StreamName = types.StringValue(alert.StreamName)
	model.IsRealTime = types.BoolValue(alert.IsRealTime)
	model.Enabled = types.BoolValue(alert.Enabled)
	model.Description = types.StringValue(alert.Description)
	model.RowTemplate = types.StringValue(alert.RowTemplate)
	model.TZOffset = types.Int64Value(int64(alert.TZOffset))
	model.Template = stringFromPtr(alert.Template)
	model.Owner = stringFromPtr(alert.Owner)
	model.Priority = int64FromPtr(alert.Priority)
	model.LastTriggeredAt = int64FromPtr(alert.LastTriggeredAt)
	model.LastSatisfiedAt = int64FromPtr(alert.LastSatisfiedAt)
	model.UpdatedAt = int64FromPtr(alert.UpdatedAt)
	model.LastEditedBy = stringFromPtr(alert.LastEditedBy)

	model.Destinations = setFromStrings(ctx, alert.Destinations, diags)
	model.ContextAttributes = mapFromStrings(ctx, alert.ContextAttrs, diags)
	if len(alert.Tags) == 0 {
		model.Tags = types.SetNull(types.StringType)
	} else {
		model.Tags = setFromStrings(ctx, alert.Tags, diags)
	}

	model.QueryCondition = queryConditionToModel(ctx, &alert.QueryCondition, diags)
	model.TriggerCondition = triggerConditionToModel(&alert.TriggerCondition)

	if alert.FolderID != nil && *alert.FolderID != "" {
		model.FolderID = types.StringValue(*alert.FolderID)
	}
}

func queryConditionToModel(ctx context.Context, api *AlertQueryConditionAPI, diags *diag.Diagnostics) *AlertQueryConditionModel {
	out := &AlertQueryConditionModel{
		QueryType:       types.StringValue(api.QueryType),
		SQL:             stringFromPtr(api.SQL),
		PromQL:          stringFromPtr(api.PromQL),
		Conditions:      jsonStringValue(api.Conditions, diags),
		VRLFunction:     stringFromPtr(api.VRLFunction),
		SearchEventType: stringFromPtr(api.SearchEventType),
	}

	if api.PromQLCondition != nil {
		out.PromQLCondition = &AlertConditionModel{
			Column:   types.StringValue(api.PromQLCondition.Column),
			Operator: types.StringValue(api.PromQLCondition.Operator),
			Value:    decodeConditionValue(api.PromQLCondition.Value),
		}
	}

	if api.Aggregation != nil {
		out.Aggregation = &AlertAggregationModel{
			GroupBy:      listFromStrings(ctx, api.Aggregation.GroupBy, diags),
			Function:     types.StringValue(api.Aggregation.Function),
			WarningValue: float64FromPtr(api.Aggregation.WarningValue),
			MultiAlert:   types.BoolValue(api.Aggregation.MultiAlert),
			Having: &AlertConditionModel{
				Column:   types.StringValue(api.Aggregation.Having.Column),
				Operator: types.StringValue(api.Aggregation.Having.Operator),
				Value:    decodeConditionValue(api.Aggregation.Having.Value),
			},
		}
	}

	for _, tr := range api.MultiTimeRange {
		out.MultiTimeRange = append(out.MultiTimeRange, AlertTimeOffsetModel{Offset: types.StringValue(tr.Offset)})
	}
	return out
}

func triggerConditionToModel(api *AlertTriggerConditionAPI) *AlertTriggerConditionModel {
	return &AlertTriggerConditionModel{
		Period:           types.Int64Value(api.Period),
		Operator:         types.StringValue(api.Operator),
		Threshold:        types.Int64Value(api.Threshold),
		WarningThreshold: int64FromPtr(api.WarningThreshold),
		NotifyOnWarning:  boolFromPtr(api.NotifyOnWarning),
		Frequency:        types.Int64Value(api.Frequency),
		FrequencyType:    types.StringValue(api.FrequencyType),
		Cron:             types.StringValue(api.Cron),
		Silence:          types.Int64Value(api.Silence),
		Timezone:         stringFromPtr(api.Timezone),
		ToleranceInSecs:  int64FromPtr(api.ToleranceInSecs),
		AlignTime:        types.BoolValue(api.AlignTime),
	}
}

func alertResourceID(orgID, alertID string) string {
	return fmt.Sprintf("%s/%s", orgID, alertID)
}
