package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
//
// The word-shaped operators are PascalCase because that is the only spelling
// the v2 alerts API accepts. Its request model declares them with no serde
// rename, unlike the internal storage model, which uses snake_case with
// PascalCase aliases. Sending `is_not_empty` is rejected outright:
//
//	unknown variant `is_not_empty`, expected one of `=`, `!=`, `>`, `>=`, `<`,
//	`<=`, `Contains`, `NotContains`, `IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`
// The last four are unary: they test a column without comparing it to
// anything, so a condition using one carries no `value`, which is why `value`
// is optional. There is no validator pairing the two, because conditions reach
// the provider as an opaque JSON string rather than as attributes.
var comparisonOperators = []string{
	"=", "!=", ">", ">=", "<", "<=",
	"Contains", "NotContains",
	"IsNull", "IsNotNull", "IsEmpty", "IsNotEmpty",
}

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
	RowTemplateType   types.String                `tfsdk:"row_template_type"`
	TZOffset          types.Int64                 `tfsdk:"tz_offset"`
	Owner             types.String                `tfsdk:"owner"`
	Priority          types.Int64                 `tfsdk:"priority"`
	Tags              types.Set                   `tfsdk:"tags"`
	CreatesIncident   types.Bool                  `tfsdk:"creates_incident"`
	Workflows         types.Set                   `tfsdk:"workflows"`
	PendingPeriodSec  types.Int64                 `tfsdk:"pending_period_sec"`
	Deduplication     *AlertDeduplicationModel    `tfsdk:"deduplication"`
	QueryCondition    *AlertQueryConditionModel   `tfsdk:"query_condition"`
	TriggerCondition  *AlertTriggerConditionModel `tfsdk:"trigger_condition"`
	LastTriggeredAt   types.Int64                 `tfsdk:"last_triggered_at"`
	LastSatisfiedAt   types.Int64                 `tfsdk:"last_satisfied_at"`
	UpdatedAt         types.Int64                 `tfsdk:"updated_at"`
	LastEditedBy      types.String                `tfsdk:"last_edited_by"`
}

// AlertQueryConditionModel is the query_condition block.
type AlertQueryConditionModel struct {
	QueryType          types.String            `tfsdk:"type"`
	SQL                types.String            `tfsdk:"sql"`
	PromQL             types.String            `tfsdk:"promql"`
	PromQLCondition    *AlertConditionModel    `tfsdk:"promql_condition"`
	PromQLWarningValue types.Float64           `tfsdk:"promql_warning_value"`
	PromQLMultiAlert   types.Bool              `tfsdk:"promql_multi_alert"`
	Conditions         types.String            `tfsdk:"conditions"`
	Aggregation        *AlertAggregationModel  `tfsdk:"aggregation"`
	SloCondition       *AlertSloConditionModel `tfsdk:"slo_condition"`
	VRLFunction        types.String            `tfsdk:"vrl_function"`
	SearchEventType    types.String            `tfsdk:"search_event_type"`
	MultiTimeRange     types.List              `tfsdk:"multi_time_range"`
}

// AlertSloConditionModel is the slo_condition block of an SLO alert.
type AlertSloConditionModel struct {
	SloID           types.String  `tfsdk:"slo_id"`
	Kind            types.String  `tfsdk:"kind"`
	Operator        types.String  `tfsdk:"operator"`
	Critical        types.Float64 `tfsdk:"critical"`
	Warning         types.Float64 `tfsdk:"warning"`
	LongWindowSecs  types.Int64   `tfsdk:"long_window_secs"`
	ShortWindowSecs types.Int64   `tfsdk:"short_window_secs"`
	MultiAlert      types.Bool    `tfsdk:"multi_alert"`
}

// AlertDeduplicationModel is the deduplication block.
type AlertDeduplicationModel struct {
	Enabled           types.Bool  `tfsdk:"enabled"`
	FingerprintFields types.Set   `tfsdk:"fingerprint_fields"`
	TimeWindowMinutes types.Int64 `tfsdk:"time_window_minutes"`
}

// AlertConditionModel is a single column/operator/value comparison.
type AlertConditionModel struct {
	Column     types.String `tfsdk:"column"`
	Operator   types.String `tfsdk:"operator"`
	Value      types.String `tfsdk:"value"`
	IgnoreCase types.Bool   `tfsdk:"ignore_case"`
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
				Required: required,
				Optional: !required,
				Description: "Comparison operator.\n\n" +
					"Comparing: `=`, `!=`, `>`, `>=`, `<`, `<=`, `Contains`, `NotContains`.\n\n" +
					"Testing the column alone: `IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`. These are " +
					"unary, so they take no `value`. `IsEmpty` also matches a null, which is usually what " +
					"you want when a field may be either absent or blank.\n\n" +
					"The word-shaped operators are PascalCase because that is the only spelling the API " +
					"accepts for them.",
				Validators: []validator.String{stringvalidator.OneOf(comparisonOperators...)},
			},
			"value": schema.StringAttribute{
				// Optional even where the block is required, because a unary
				// operator has nothing to compare against. ValidateConfig
				// requires it for every other operator.
				Optional: true,
				Description: "Value to compare against. A value that parses as a number is sent as a JSON number; " +
					"anything else is sent as a JSON string.\n\n" +
					"Omit it with a unary operator (`IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`), which " +
					"tests the column itself.",
			},
			"ignore_case": schema.BoolAttribute{
				Optional:    true,
				Description: "Compare case-insensitively. Only meaningful for string comparisons.",
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
			"row_template_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("String"),
				Description: "How `row_template` is rendered: `String` (default) or `Json`.",
				Validators:  []validator.String{stringvalidator.OneOf("String", "Json")},
			},
			"creates_incident": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Route firings through the incident system instead of sending direct notifications.",
			},
			"workflows": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Workflow IDs to trigger when the alert fires.",
			},
			"pending_period_sec": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				Description: "How long the condition must hold before the alert fires, in seconds.\n\n" +
					"Zero, the default, fires on the first evaluation that breaches. A pending period rides out " +
					"a brief spike: the condition has to still be true this many seconds later. It is the " +
					"difference between paging on one slow minute and paging on a sustained problem.\n\n" +
					"Not accepted on a real-time alert, which has no schedule to wait across.",
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
						Optional: true,
						Computed: true,
						Default:  stringdefault.StaticString("custom"),
						Description: "Query style: `custom` (UI-style conditions, default), `sql`, `promql`, or " +
							"`slo` (reads a precomputed SLO's status instead of running a query).",
						Validators: []validator.String{stringvalidator.OneOf("custom", "sql", "promql", "slo")},
					},
					"sql": schema.StringAttribute{
						Optional:    true,
						Description: "SQL query returning an aggregate to compare against the threshold. Required when `type` is `sql`.",
					},
					"promql": schema.StringAttribute{
						Optional:    true,
						Description: "PromQL expression. Required when `type` is `promql`.",
					},
					"promql_warning_value": schema.Float64Attribute{
						Optional: true,
						Description: "Warning-level value for the PromQL comparison, sharing the operator of " +
							"`promql_condition`. Omit for a single-level alert.",
					},
					"promql_multi_alert": schema.BoolAttribute{
						Optional: true,
						Description: "Evaluate and notify per returned series rather than collapsing the query to " +
							"one verdict. The group key is the series' full label set, chosen by the expression's " +
							"own `by (…)` clause. This is PromQL's counterpart to `aggregation.multi_alert`.",
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
								Computed:    true,
								ElementType: types.StringType,
								Description: "Columns to group by before aggregating.\n\n" +
									"On a `sql` multi-alert this can be left empty and the server fills it in " +
									"from the query's own GROUP BY, so the value read back may differ from " +
									"the empty list that was sent.",
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
								Optional: true,
								Description: "Evaluate and notify per group instead of collapsing to a single result.\n\n" +
									"For a `custom` alert the group is the `group_by` column set, which must be non-empty. " +
									"A `sql` alert may leave `group_by` empty: the grouping comes from the query's own " +
									"GROUP BY, and `having.column` names the column carrying the value to compare.",
							},
						},
						Blocks: map[string]schema.Block{
							"having": schema.SingleNestedBlock{
								Description: "Critical threshold applied to the aggregate value.",
								Attributes:  conditionAttributes(false),
							},
						},
					},
					"slo_condition": schema.SingleNestedBlock{
						Description: "What to watch on an SLO. Required when `type` is `slo`.\n\n" +
							"An SLO alert reads a precomputed objective rather than running its own query, so it " +
							"costs nothing to evaluate and fires on the same numbers the SLO page shows.",
						Attributes: map[string]schema.Attribute{
							"slo_id": schema.StringAttribute{
								Optional:    true,
								Description: "Objective to watch, as produced by `openobserve_slo.slo_id`.",
							},
							"kind": schema.StringAttribute{
								Optional: true,
								Description: "What the threshold applies to: `error_budget` (percentage of the " +
									"budget consumed over the window) or `burn_rate` (multiples of the " +
									"budget-neutral rate, evaluated in two windows).",
								Validators: []validator.String{stringvalidator.OneOf("error_budget", "burn_rate")},
							},
							"operator": schema.StringAttribute{
								Optional: true,
								Description: "Comparison against the thresholds. Ascending only, `>` or `>=`, " +
									"because both budget consumption and burn rate are bad when high.",
								Validators: []validator.String{stringvalidator.OneOf(">", ">=")},
							},
							"critical": schema.Float64Attribute{
								Optional:    true,
								Description: "Critical threshold. Must be finite and strictly positive.",
							},
							"warning": schema.Float64Attribute{
								Optional:    true,
								Description: "Warning threshold, sharing `operator` with the critical one. Omit for a single-level alert.",
							},
							"long_window_secs": schema.Int64Attribute{
								Optional: true,
								Description: "`burn_rate` only. The long evaluation window: 1h to 48h, no longer " +
									"than the SLO window, and an exact multiple of at least twice the slice interval.",
							},
							"short_window_secs": schema.Int64Attribute{
								Optional: true,
								Description: "`burn_rate` only, and required alongside `long_window_secs`. A " +
									"twelfth of the long window is the conventional choice, following the " +
									"Google SRE workbook, but it must also be at least twice the SLO's slice " +
									"interval: a one-slice window has coverage 0 or 1, so a single gap would " +
									"freeze the alert.",
							},
							"multi_alert": schema.BoolAttribute{
								Optional: true,
								Description: "Evaluate and notify per SLO group rather than on the objective as a " +
									"whole. Requires a grouped SLO.",
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
			"deduplication": schema.SingleNestedBlock{
				Description: "Collapse repeated firings of the same underlying issue into one notification.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Optional:    true,
						Description: "Whether deduplication is applied.",
					},
					"fingerprint_fields": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "Fields from the query result that identify one issue. Left empty, the server " +
							"infers them from the query: the condition fields for `custom`, the GROUP BY columns " +
							"for `sql`, and the label dimensions for `promql`.",
					},
					"time_window_minutes": schema.Int64Attribute{
						Optional:    true,
						Description: "How long a fingerprint stays suppressed. Defaults to twice the alert frequency.",
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
						Optional: true,
						Computed: true,
						Description: "Operator comparing the result to `threshold`. Defaults to `>=`.\n\n" +
							"Leave unset on an SLO alert: that family has no count gate, and its comparison " +
							"lives on `query_condition.slo_condition.operator`.",
						Validators: []validator.String{stringvalidator.OneOf(comparisonOperators...)},
					},
					"threshold": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Critical threshold the result is compared against. Defaults to `1`.\n\n" +
							"Leave unset on an SLO alert: that family has no count gate, and its threshold " +
							"lives on `query_condition.slo_condition.critical`.",
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
	case "slo":
		if config.QueryCondition.SloCondition == nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("query_condition").AtName("slo_condition"),
				"Missing slo_condition",
				"`slo_condition` is required when `query_condition.type` is `slo`.",
			)
			break
		}
		// The trigger threshold is a count gate for every other family. An SLO
		// alert has none, so setting one means the author expected a behaviour
		// they will not get.
		if config.TriggerCondition != nil {
			if knownAndSet(config.TriggerCondition.Threshold) {
				resp.Diagnostics.AddAttributeError(
					path.Root("trigger_condition").AtName("threshold"),
					"SLO alerts have no count gate",
					"An SLO alert is thresholded by `query_condition.slo_condition.critical`, not by "+
						"`trigger_condition.threshold`. Remove the threshold.",
				)
			}
			if knownAndSet(config.TriggerCondition.Operator) {
				resp.Diagnostics.AddAttributeError(
					path.Root("trigger_condition").AtName("operator"),
					"SLO alerts have no count gate",
					"An SLO alert is compared by `query_condition.slo_condition.operator`, not by "+
						"`trigger_condition.operator`. Remove the operator.",
				)
			}
		}

		// The two windows only exist for burn-rate alerts; setting them on a
		// budget alert means the author expected a behaviour they will not get.
		if config.QueryCondition.SloCondition.Kind.ValueString() == "error_budget" &&
			(knownAndSet(config.QueryCondition.SloCondition.LongWindowSecs) ||
				knownAndSet(config.QueryCondition.SloCondition.ShortWindowSecs)) {
			resp.Diagnostics.AddAttributeError(
				path.Root("query_condition").AtName("slo_condition"),
				"Windows apply only to burn-rate alerts",
				"`long_window_secs` and `short_window_secs` are only meaningful when `kind` is `burn_rate`. "+
					"An `error_budget` alert measures over the SLO's own window.",
			)
		}
	}

	if config.QueryCondition.Aggregation != nil && config.TriggerCondition != nil &&
		knownAndSet(config.TriggerCondition.WarningThreshold) {
		resp.Diagnostics.AddAttributeError(
			path.Root("trigger_condition").AtName("warning_threshold"),
			"warning_threshold is not supported on aggregation alerts",
			"On an aggregation alert the count threshold is coverage, not severity. Set the warning level "+
				"with `query_condition.aggregation.warning_value` instead.",
		)
	}

	if sc := config.QueryCondition.SloCondition; sc != nil && sc.Kind.ValueString() == "burn_rate" {
		// Both windows are required. The provider deliberately does not derive
		// the short one: the minimum is two slice intervals, and the slice
		// interval belongs to the SLO, so any value guessed here could be
		// rejected.
		if (sc.LongWindowSecs.IsNull() && !sc.LongWindowSecs.IsUnknown()) ||
			(sc.ShortWindowSecs.IsNull() && !sc.ShortWindowSecs.IsUnknown()) {
			resp.Diagnostics.AddAttributeError(
				path.Root("query_condition").AtName("slo_condition"),
				"Burn-rate alerts need both windows",
				"Set `long_window_secs` and `short_window_secs`. A twelfth of the long window is the "+
					"conventional starting point, but the short window must also be at least twice the "+
					"SLO's slice interval. A one-slice window has coverage 0 or 1, so a single gap would "+
					"freeze the alert.",
			)
		}
	}

	// The server rejects both of these rather than clamping, so catching them
	// here turns an apply-time 400 into a diagnostic on the offending line.
	if knownAndSet(config.PendingPeriodSec) {
		if config.PendingPeriodSec.ValueInt64() < 0 {
			resp.Diagnostics.AddAttributeError(
				path.Root("pending_period_sec"),
				"Pending period cannot be negative",
				"`pending_period_sec` must be zero or greater. Zero fires on the first breaching evaluation.",
			)
		}
		if config.PendingPeriodSec.ValueInt64() != 0 && config.IsRealTime.ValueBool() {
			resp.Diagnostics.AddAttributeError(
				path.Root("pending_period_sec"),
				"Pending period does not apply to a real-time alert",
				"A real-time alert evaluates as each record arrives, so there is no schedule to wait across. "+
					"Remove `pending_period_sec`, or drop `is_real_time`.",
			)
		}
	}

	if config.TriggerCondition != nil &&
		config.TriggerCondition.FrequencyType.ValueString() == "cron" &&
		config.TriggerCondition.Cron.IsNull() && !config.TriggerCondition.Cron.IsUnknown() {
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
	// A composite alert holding this one as a child blocks the delete, so
	// annotate that refusal rather than passing the bare error code through.
	if err := r.client.DeleteAlert(ctx, org, state.AlertID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting alert", compositeErrorDetail(err))
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
		Name:             model.Name.ValueString(),
		OrgID:            org,
		StreamType:       model.StreamType.ValueString(),
		StreamName:       model.StreamName.ValueString(),
		IsRealTime:       model.IsRealTime.ValueBool(),
		Destinations:     destinations,
		Template:         optString(model.Template),
		ContextAttrs:     stringsFromMap(ctx, model.ContextAttributes, diags),
		RowTemplate:      model.RowTemplate.ValueString(),
		RowTemplateType:  model.RowTemplateType.ValueString(),
		CreatesIncident:  model.CreatesIncident.ValueBool(),
		Workflows:        stringsFromSet(ctx, model.Workflows, diags),
		Description:      model.Description.ValueString(),
		Enabled:          model.Enabled.ValueBool(),
		TZOffset:         int32(model.TZOffset.ValueInt64()),
		Owner:            optString(model.Owner),
		Priority:         optInt64(model.Priority),
		PendingPeriodSec: model.PendingPeriodSec.ValueInt64(),
		Tags:             stringsFromSet(ctx, model.Tags, diags),
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
	// The count gate's defaults depend on the family. Every other family gates
	// on "at least one result"; an SLO alert has no gate at all, and the server
	// rejects a non-default threshold rather than ignoring it.
	if model.TriggerCondition != nil {
		isSlo := alert.QueryCondition.QueryType == "slo"
		if model.TriggerCondition.Operator.IsNull() || model.TriggerCondition.Operator.IsUnknown() {
			if isSlo {
				alert.TriggerCondition.Operator = "="
			} else {
				alert.TriggerCondition.Operator = ">="
			}
		}
		if model.TriggerCondition.Threshold.IsNull() || model.TriggerCondition.Threshold.IsUnknown() {
			if isSlo {
				alert.TriggerCondition.Threshold = 0
			} else {
				alert.TriggerCondition.Threshold = 1
			}
		}
	}
	if model.Deduplication != nil {
		alert.Deduplication = &AlertDeduplicationAPI{
			Enabled:           model.Deduplication.Enabled.ValueBool(),
			FingerprintFields: stringsFromSet(ctx, model.Deduplication.FingerprintFields, diags),
			TimeWindowMinutes: optInt64(model.Deduplication.TimeWindowMinutes),
		}
	}
	return alert
}

func queryConditionFromModel(ctx context.Context, model *AlertQueryConditionModel, diags *diag.Diagnostics) AlertQueryConditionAPI {
	out := AlertQueryConditionAPI{
		QueryType:          model.QueryType.ValueString(),
		SQL:                optString(model.SQL),
		PromQL:             optString(model.PromQL),
		PromQLWarningValue: optFloat64(model.PromQLWarningValue),
		PromQLMultiAlert:   model.PromQLMultiAlert.ValueBool(),
		VRLFunction:        optString(model.VRLFunction),
		SearchEventType:    optString(model.SearchEventType),
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

	if model.SloCondition != nil && !model.SloCondition.SloID.IsNull() {
		out.SloCondition = &AlertSloConditionAPI{
			SloID:           model.SloCondition.SloID.ValueString(),
			Kind:            model.SloCondition.Kind.ValueString(),
			Operator:        model.SloCondition.Operator.ValueString(),
			Critical:        model.SloCondition.Critical.ValueFloat64(),
			Warning:         optFloat64(model.SloCondition.Warning),
			LongWindowSecs:  optInt64(model.SloCondition.LongWindowSecs),
			ShortWindowSecs: optInt64(model.SloCondition.ShortWindowSecs),
			MultiAlert:      model.SloCondition.MultiAlert.ValueBool(),
		}
	}

	// multi_time_range is a types.List rather than a Go slice because a slice
	// cannot represent an unknown value, and Terraform hands the provider an
	// unknown list whenever a `dynamic "multi_time_range"` block's for_each
	// comes from a variable. Decoding into a slice fails config decode outright
	// with "Received unknown value, however the target type cannot handle
	// unknown values", before ValidateConfig ever runs. That is
	// openobserve/terraform-provider-openobserve#2.
	//
	// An unknown list contributes nothing here: the values resolve before
	// apply, and the write path only ever runs with a concrete plan.
	if knownAndSet(model.MultiTimeRange) {
		var ranges []AlertTimeOffsetModel
		diags.Append(model.MultiTimeRange.ElementsAs(ctx, &ranges, false)...)
		for _, tr := range ranges {
			out.MultiTimeRange = append(out.MultiTimeRange, AlertCompareHistoricData{Offset: tr.Offset.ValueString()})
		}
	}
	return out
}

func conditionFromModel(model *AlertConditionModel, diags *diag.Diagnostics) AlertConditionAPI {
	return AlertConditionAPI{
		Column:     model.Column.ValueString(),
		Operator:   model.Operator.ValueString(),
		Value:      encodeConditionValue(model.Value.ValueString(), diags),
		IgnoreCase: model.IgnoreCase.ValueBool(),
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

// conditionValueToModel renders a threshold back, keeping the configured value
// null when the server reports an empty one.
//
// A unary operator needs no value, but the server's Condition carries a
// non-optional `value`, so it stores and echoes an empty string. Writing that
// back where the configuration deliberately said nothing is an inconsistent
// result after apply, and would report drift forever besides.
func conditionValueToModel(prior types.String, raw json.RawMessage) types.String {
	decoded := decodeConditionValue(raw)
	if prior.IsNull() && decoded.ValueString() == "" {
		return types.StringNull()
	}
	return decoded
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
	model.CreatesIncident = types.BoolValue(alert.CreatesIncident)
	if alert.RowTemplateType != "" {
		model.RowTemplateType = types.StringValue(alert.RowTemplateType)
	}
	if len(alert.Workflows) == 0 {
		model.Workflows = types.SetNull(types.StringType)
	} else {
		model.Workflows = setFromStrings(ctx, alert.Workflows, diags)
	}
	if alert.Deduplication != nil {
		model.Deduplication = &AlertDeduplicationModel{
			Enabled:           types.BoolValue(alert.Deduplication.Enabled),
			TimeWindowMinutes: int64FromPtr(alert.Deduplication.TimeWindowMinutes),
		}
		if len(alert.Deduplication.FingerprintFields) == 0 {
			model.Deduplication.FingerprintFields = types.SetNull(types.StringType)
		} else {
			model.Deduplication.FingerprintFields = setFromStrings(ctx, alert.Deduplication.FingerprintFields, diags)
		}
	}
	model.TZOffset = types.Int64Value(int64(alert.TZOffset))
	model.PendingPeriodSec = types.Int64Value(alert.PendingPeriodSec)
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

	model.QueryCondition = queryConditionToModel(ctx, &alert.QueryCondition, model.QueryCondition, diags)
	model.TriggerCondition = triggerConditionToModel(&alert.TriggerCondition)

	if alert.FolderID != nil && *alert.FolderID != "" {
		model.FolderID = types.StringValue(*alert.FolderID)
	}
}

func queryConditionToModel(ctx context.Context, api *AlertQueryConditionAPI, prior *AlertQueryConditionModel, diags *diag.Diagnostics) *AlertQueryConditionModel {
	// Optional booleans are only reported when the server says true, or when
	// the configuration already set them; see boolPreserveNull.
	var priorPromQLMultiAlert, priorAggMultiAlert, priorSloMultiAlert types.Bool
	if prior != nil {
		priorPromQLMultiAlert = prior.PromQLMultiAlert
		if prior.Aggregation != nil {
			priorAggMultiAlert = prior.Aggregation.MultiAlert
		}
		if prior.SloCondition != nil {
			priorSloMultiAlert = prior.SloCondition.MultiAlert
		}
	}
	var priorHavingIgnoreCase types.Bool
	var priorHavingValue types.String
	if prior != nil && prior.Aggregation != nil && prior.Aggregation.Having != nil {
		priorHavingIgnoreCase = prior.Aggregation.Having.IgnoreCase
		priorHavingValue = prior.Aggregation.Having.Value
	}

	out := &AlertQueryConditionModel{
		QueryType:          types.StringValue(api.QueryType),
		SQL:                stringFromPtr(api.SQL),
		PromQL:             stringFromPtr(api.PromQL),
		PromQLWarningValue: float64FromPtr(api.PromQLWarningValue),
		PromQLMultiAlert:   boolPreserveNull(priorPromQLMultiAlert, api.PromQLMultiAlert),
		Conditions:         jsonStringValue(api.Conditions, diags),
		VRLFunction:        stringFromPtr(api.VRLFunction),
		SearchEventType:    stringFromPtr(api.SearchEventType),
	}

	if api.SloCondition != nil {
		out.SloCondition = &AlertSloConditionModel{
			SloID:           types.StringValue(api.SloCondition.SloID),
			Kind:            types.StringValue(api.SloCondition.Kind),
			Operator:        types.StringValue(api.SloCondition.Operator),
			Critical:        types.Float64Value(api.SloCondition.Critical),
			Warning:         float64FromPtr(api.SloCondition.Warning),
			LongWindowSecs:  int64FromPtr(api.SloCondition.LongWindowSecs),
			ShortWindowSecs: int64FromPtr(api.SloCondition.ShortWindowSecs),
			MultiAlert:      boolPreserveNull(priorSloMultiAlert, api.SloCondition.MultiAlert),
		}
	}

	if api.PromQLCondition != nil {
		var priorPromQLIgnoreCase types.Bool
		var priorPromQLValue types.String
		if prior != nil && prior.PromQLCondition != nil {
			priorPromQLIgnoreCase = prior.PromQLCondition.IgnoreCase
			priorPromQLValue = prior.PromQLCondition.Value
		}
		out.PromQLCondition = &AlertConditionModel{
			Column:     types.StringValue(api.PromQLCondition.Column),
			Operator:   types.StringValue(api.PromQLCondition.Operator),
			Value:      conditionValueToModel(priorPromQLValue, api.PromQLCondition.Value),
			IgnoreCase: boolPreserveNull(priorPromQLIgnoreCase, api.PromQLCondition.IgnoreCase),
		}
	}

	if api.Aggregation != nil {
		// A SQL multi-alert may be configured with no group_by at all, and the
		// server answers with the columns it derived from the query. Taking the
		// server's list is right: it is what the alert actually groups by, and
		// the attribute is Computed so that it can differ from the config.
		out.Aggregation = &AlertAggregationModel{
			GroupBy:      listFromStrings(ctx, api.Aggregation.GroupBy, diags),
			Function:     types.StringValue(api.Aggregation.Function),
			WarningValue: float64FromPtr(api.Aggregation.WarningValue),
			MultiAlert:   boolPreserveNull(priorAggMultiAlert, api.Aggregation.MultiAlert),
			Having: &AlertConditionModel{
				Column:     types.StringValue(api.Aggregation.Having.Column),
				Operator:   types.StringValue(api.Aggregation.Having.Operator),
				Value:      conditionValueToModel(priorHavingValue, api.Aggregation.Having.Value),
				IgnoreCase: boolPreserveNull(priorHavingIgnoreCase, api.Aggregation.Having.IgnoreCase),
			},
		}
	}

	out.MultiTimeRange = multiTimeRangeToList(ctx, api.MultiTimeRange, diags)
	return out
}

// multiTimeRangeAttrTypes describes one multi_time_range element.
var multiTimeRangeAttrTypes = map[string]attr.Type{"offset": types.StringType}

// multiTimeRangeToList renders the API's offsets as a list value, using a null
// list rather than an empty one when the server reports none. An empty list
// would differ from the null Terraform holds for a block that was never
// written, and would show as drift on every plan.
func multiTimeRangeToList(ctx context.Context, api []AlertCompareHistoricData, diags *diag.Diagnostics) types.List {
	elemType := types.ObjectType{AttrTypes: multiTimeRangeAttrTypes}
	if len(api) == 0 {
		return types.ListNull(elemType)
	}
	values := make([]attr.Value, 0, len(api))
	for _, tr := range api {
		values = append(values, objectValue(multiTimeRangeAttrTypes, map[string]attr.Value{
			"offset": types.StringValue(tr.Offset),
		}, diags))
	}
	list, d := types.ListValue(elemType, values)
	diags.Append(d...)
	return list
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
