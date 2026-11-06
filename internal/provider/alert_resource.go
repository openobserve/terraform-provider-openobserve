package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
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
	_ resource.ResourceWithModifyPlan     = &AlertResource{}
)

// thresholdOperators are what `trigger_condition.operator` accepts.
//
// A threshold compares a row count against a number, so only the symbols mean
// anything. The schema used to allow the word-shaped operators here too, which
// the server accepts at the API boundary and then fails on while storing:
//
//	HTTP 500: DbError# PutAlert# cannot convert contains into a trigger
//	threshold operator
//
// A 500 gives the user nothing to act on, so the narrower set is enforced
// during `plan`.
var thresholdOperators = []string{"=", "!=", ">", ">=", "<", "<="}

// comparisonOperators are what the column comparison attributes accept: an
// aggregation's `having` and a PromQL condition.
//
// The word-shaped forms are PascalCase because that is the only spelling the
// v2 alerts API accepts for these. Its request model declares the enum with no
// serde rename, so `is_not_empty` is rejected outright:
//
//	unknown variant `is_not_empty`, expected one of `=`, `!=`, `>`, `>=`, `<`,
//	`<=`, `Contains`, `NotContains`, `IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`
//
// The two attributes are not interchangeable despite sharing this list.
// `having.value` must always parse as a number, whatever the operator, so a
// unary operator is unusable there: the provider sends `""` for an omitted
// value and the server answers `400: aggregation threshold (having.value) is
// not numeric`. On a PromQL condition the same omission is accepted.
//
// The `conditions` document is governed by none of this. It reaches the server
// as written and is parsed by the storage model behind the untagged V1
// `ConditionList`, which accepts either spelling, requires `value` to be
// present, and rewrites what it stores. See canonicalConditionOperators.
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
	ID                types.String `tfsdk:"id"`
	AlertID           types.String `tfsdk:"alert_id"`
	OrgID             types.String `tfsdk:"org_id"`
	FolderID          types.String `tfsdk:"folder_id"`
	Name              types.String `tfsdk:"name"`
	StreamType        types.String `tfsdk:"stream_type"`
	StreamName        types.String `tfsdk:"stream_name"`
	IsRealTime        types.Bool   `tfsdk:"is_real_time"`
	Enabled           types.Bool   `tfsdk:"enabled"`
	Description       types.String `tfsdk:"description"`
	Destinations      types.Set    `tfsdk:"destinations"`
	Template          types.String `tfsdk:"template"`
	ContextAttributes types.Map    `tfsdk:"context_attributes"`
	RowTemplate       types.String `tfsdk:"row_template"`
	RowTemplateType   types.String `tfsdk:"row_template_type"`
	TZOffset          types.Int64  `tfsdk:"tz_offset"`
	Owner             types.String `tfsdk:"owner"`
	Priority          types.Int64  `tfsdk:"priority"`
	Tags              types.Set    `tfsdk:"tags"`
	CreatesIncident   types.Bool   `tfsdk:"creates_incident"`
	Workflows         types.Set    `tfsdk:"workflows"`
	PendingPeriodSec  types.Int64  `tfsdk:"pending_period_sec"`
	NotifyOnRecovery  types.Bool   `tfsdk:"notify_on_recovery"`
	// The settings below are Optional+Computed with no default: unset, an
	// update sends what the server holds. See keepUnmanagedSettings.
	RecoveryDestinations types.Set                   `tfsdk:"recovery_destinations"`
	KeepFiringFor        types.Int64                 `tfsdk:"keep_firing_for"`
	SuppressRepeats      types.Bool                  `tfsdk:"suppress_repeats"`
	RunbookURL           types.String                `tfsdk:"runbook_url"`
	OncallTeam           types.String                `tfsdk:"oncall_team"`
	Deduplication        *AlertDeduplicationModel    `tfsdk:"deduplication"`
	QueryCondition       *AlertQueryConditionModel   `tfsdk:"query_condition"`
	TriggerCondition     *AlertTriggerConditionModel `tfsdk:"trigger_condition"`
	LastTriggeredAt      types.Int64                 `tfsdk:"last_triggered_at"`
	LastSatisfiedAt      types.Int64                 `tfsdk:"last_satisfied_at"`
	UpdatedAt            types.Int64                 `tfsdk:"updated_at"`
	LastEditedBy         types.String                `tfsdk:"last_edited_by"`
}

// AlertQueryConditionModel is the query_condition block.
type AlertQueryConditionModel struct {
	QueryType          types.String            `tfsdk:"type"`
	SQL                types.String            `tfsdk:"sql"`
	PromQL             types.String            `tfsdk:"promql"`
	PromQLCondition    *AlertConditionModel    `tfsdk:"promql_condition"`
	PromQLWarningValue types.Float64           `tfsdk:"promql_warning_value"`
	PromQLMultiAlert   types.Bool              `tfsdk:"promql_multi_alert"`
	PromQLRuleMode     types.Bool              `tfsdk:"promql_rule_mode"`
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
	// numericValue distinguishes `having` from `promql_condition`. They share a
	// shape but not a contract: an aggregate is a number, so `having.value`
	// must always parse as one, and a unary operator with no value earns
	// `400: aggregation threshold (having.value) is not numeric`. A PromQL
	// condition accepts the omission.
	conditionAttributes := func(required, numericValue bool) map[string]schema.Attribute {
		unaryNote := "Testing the column alone: `IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`. These are " +
			"unary, so they take no `value`. `IsEmpty` also matches a null, which is usually what " +
			"you want when a field may be either absent or blank.\n\n"
		valueNote := "Omit it with a unary operator (`IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`), which " +
			"tests the column itself."
		if numericValue {
			unaryNote = "`IsNull`, `IsNotNull`, `IsEmpty` and `IsNotEmpty` are accepted but of little use " +
				"here, because `value` is still required and still has to be a number.\n\n"
			valueNote = "Required here, and it must parse as a number: an aggregate is a number, so the " +
				"server rejects anything else with `400: aggregation threshold (having.value) is not " +
				"numeric`. This differs from `promql_condition`, where a unary operator may omit it."
		}
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
					unaryNote +
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
					valueNote,
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
			"alerts driven by `trigger_condition.frequency`.\n\n" +
			"OpenObserve replaces the whole alert on update. Settings left unset are read from the server before " +
			"each update and sent back unchanged, so a value set in the UI or through the API survives: " +
			"`notify_on_recovery`, `recovery_destinations`, `keep_firing_for`, `suppress_repeats`, `runbook_url`, " +
			"`oncall_team`, `destinations` and `query_condition.promql_rule_mode`, and the incident title template, " +
			"which this resource does not manage yet. See `notify_on_recovery` for the cases where the server " +
			"forces some of them off.",
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
				// Unset, the plan carries the list last read and the update sends
				// the server's; see keepUnmanagedSettings.
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Description: "Names of the `openobserve_alert_destination` resources notified when the alert fires.\n\n" +
					"Leave it unset to keep whatever the server holds across updates, so destinations added in " +
					"the UI or through the API are not removed by an unrelated change. Set it to manage the " +
					"list, and a change made elsewhere then shows as drift.",
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
			"notify_on_recovery": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				// No default, deliberately. The server replaces the whole alert on
				// update, so a default would turn the flag off on every alert that
				// had it switched on outside Terraform. Unset, the plan carries the
				// value last read and the update sends whatever the server holds;
				// see keepUnmanagedSettings.
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Description: "Send a notification when a firing alert recovers, that is, when its condition " +
					"clears. A multi-alert sends one per recovered group.\n\n" +
					"Leave it unset to keep whatever the server holds across updates: the provider reads the " +
					"current value and sends it back on every update, so a flag switched on in the UI or through " +
					"the API is not turned off by an unrelated change. Set it to manage the flag, and a change " +
					"made elsewhere then shows as drift. An alert created without it, including one Terraform " +
					"replaces (a change to `stream_type` or `stream_name`), starts from the server default, " +
					"`false`, so configure it where it must survive a replacement. Removing the attribute from a " +
					"configuration that set it stops managing it and leaves the server value as it is.\n\n" +
					"While it is `true`, the server checks the recovery path on every save, update included, " +
					"and refuses one it could not deliver, for example a PagerDuty destination with no " +
					"integration key, or a platform destination whose template does not render a JSON object. " +
					"An update that left the flag on can therefore fail on a destination that has not changed.\n\n" +
					"The value is read again at apply time, so if it changed on the server after the plan was " +
					"made (or the plan used `-refresh=false`), the update still sends the server's value. State " +
					"records the planned value for that apply and picks up the server's on the next refresh, " +
					"with no diff.\n\n" +
					"`recovery_destinations`, `keep_firing_for` and `suppress_repeats` work the same way when " +
					"unset, with two exceptions the server forces: an unset `recovery_destinations` is dropped " +
					"when `notify_on_recovery` is `false`, and all three are dropped on a real-time alert.\n\n" +
					"Not accepted on a real-time alert, which keeps no state to recover from. Making an alert " +
					"real-time while the server holds `true` and this attribute is unset plans cleanly but fails " +
					"at apply, asking for `notify_on_recovery = false`.",
			},
			"recovery_destinations": schema.SetAttribute{
				Optional:      true,
				Computed:      true,
				ElementType:   types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Description: "Destinations the recovery notification goes to, when it should not follow the firing. " +
					"Empty sends it to the alert's own `destinations`. A PagerDuty, Opsgenie or ServiceNow " +
					"destination recovers by closing the ticket its own firing opened, so the server refuses one " +
					"here unless it is also in `destinations`, where it already receives the recovery.\n\n" +
					"Needs `notify_on_recovery = true`, and is not accepted on a real-time alert. " + keepServerValueDoc,
			},
			"keep_firing_for": schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
				Description: "Seconds the condition must stay clear before a firing alert recovers, from 0 to " +
					"86400 (one day). 0, the server default, recovers on the first clear evaluation. Like " +
					"Prometheus' `keep_firing_for`, it stops a flapping condition from sending a recovery and a " +
					"new firing every few minutes.\n\n" +
					"Not accepted on a real-time alert. " + keepServerValueDoc,
			},
			"suppress_repeats": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Description: "Notify once per state change instead of on every evaluation that fires: once when " +
					"the alert starts firing (or its level changes) and, with `notify_on_recovery`, once when it " +
					"recovers. `silence` is still checked first.\n\n" +
					"Honored by OpenObserve Enterprise only; other builds ignore it. Not accepted on a real-time " +
					"alert. " + keepServerValueDoc,
			},
			"runbook_url": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{keepStateWhenUnset{}},
				Validators:    []validator.String{runbookURLValidator{}},
				Description: "Link to the runbook for this alert, copied onto every on-call page it opens. Must be " +
					"an `http://` or `https://` URL with a host, at most 2048 characters. Set it to `\"\"` to " +
					"remove the link. " + keepServerValueDoc,
			},
			"oncall_team": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{keepStateWhenUnset{}},
				Validators:    []validator.String{clearableTrimmedValidator{}},
				Description: "ID of the on-call team this alert pages, overriding the team the server would find " +
					"from the alert's dimensions. The server refuses an ID that is not an on-call team of the " +
					"organization. Set it to `\"\"` to go back to ownership discovery. Needs OpenObserve " +
					"Enterprise with on-call. " + keepServerValueDoc,
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
					"promql_rule_mode": schema.BoolAttribute{
						Optional:      true,
						Computed:      true,
						PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
						Description: "Evaluate `promql` the way a Prometheus alerting rule does: every series the " +
							"expression returns is firing, so the comparison belongs in the expression itself " +
							"(`rate(errors[5m]) > 0.1`). Needs `type = \"promql\"` and `stream_type = \"metrics\"`, " +
							"and the server refuses it together with `promql_condition`, `promql_warning_value`, " +
							"`aggregation`, `multi_time_range`, `trigger_condition.warning_threshold` or a trigger " +
							"other than `>= 1` (the default) or `> 0`. With a `pending_period_sec`, set " +
							"`promql_multi_alert = true` too: the server turns it on so that each series waits out " +
							"its own pending period.\n\n" +
							"Leave it unset to keep whatever the server holds across updates, as with " +
							"`notify_on_recovery`. It is dropped when the alert stops being a PromQL alert.",
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
						Description: "Comparison applied to the PromQL result. Required when `type` is `promql`, unless " +
							"`promql_rule_mode` is on, which refuses it.",
						Attributes: conditionAttributes(false, false),
					},
					"aggregation": schema.SingleNestedBlock{
						Description: "Aggregation applied before the threshold comparison. Only used when `type` is `custom`.",
						Attributes: map[string]schema.Attribute{
							"group_by": schema.ListAttribute{
								Optional:      true,
								Computed:      true,
								ElementType:   types.StringType,
								PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
								Description: "Columns to group by before aggregating.\n\n" +
									"On a `sql` multi-alert this can be left empty and the server fills it in " +
									"from the query's own GROUP BY, so the value read back may differ from " +
									"the empty list that was sent.\n\n" +
									"Left unset on an existing alert, the server's list is kept across updates.",
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
								Attributes:  conditionAttributes(false, true),
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
						Optional:      true,
						Computed:      true,
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
						Description: "Operator comparing the result to `threshold`. Defaults to `>=` on a new alert; left " +
							"unset on an existing one, the server's operator is kept across updates.\n\n" +
							"Only the symbol operators are accepted here. A threshold compares a row count " +
							"against a number, so `Contains` and the other word-shaped operators have no " +
							"meaning; the server answers `HTTP 500: cannot convert contains into a trigger " +
							"threshold operator`, which is why this is rejected during `plan` instead.\n\n" +
							"Leave unset on an SLO alert: that family has no count gate, and its comparison " +
							"lives on `query_condition.slo_condition.operator`.",
						Validators: []validator.String{stringvalidator.OneOf(thresholdOperators...)},
					},
					"threshold": schema.Int64Attribute{
						Optional:      true,
						Computed:      true,
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
						Description: "Critical threshold the result is compared against. Defaults to `1` on a new alert; " +
							"left unset on an existing one, the server's threshold is kept across updates.\n\n" +
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
	if resp.Diagnostics.HasError() {
		return
	}
	validateRecoverySettings(&config, &resp.Diagnostics)
	if config.QueryCondition == nil {
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

	// `having` compares an aggregate, which is a number, so its value has to be
	// one whatever the operator. Omitting it sends `""` and a non-numeric value
	// is passed through as a JSON string; either way the server answers
	//
	//	400: Invalid aggregation warning value: aggregation threshold
	//	     (having.value) is not numeric
	//
	// which names a field the configuration does not mention. Catching it here
	// keeps the diagnostic on the attribute the user actually wrote.
	if agg := config.QueryCondition.Aggregation; agg != nil && agg.Having != nil {
		havingValue := path.Root("query_condition").AtName("aggregation").AtName("having").AtName("value")
		switch {
		case agg.Having.Value.IsUnknown():
			// Resolved at apply; nothing to check.
		case agg.Having.Value.IsNull():
			resp.Diagnostics.AddAttributeError(
				havingValue,
				"having requires a numeric value",
				"An aggregation's `having` compares the aggregate, which is a number, so `value` is "+
					"required even with a unary operator such as `IsNotEmpty`. Only "+
					"`query_condition.promql_condition` allows the omission.",
			)
		default:
			if _, err := strconv.ParseFloat(agg.Having.Value.ValueString(), 64); err != nil {
				resp.Diagnostics.AddAttributeError(
					havingValue,
					"having value must be numeric",
					fmt.Sprintf("`value` is %q, which is not a number. An aggregation's `having` compares "+
						"the aggregate, so the server rejects anything that does not parse as one.",
						agg.Having.Value.ValueString()),
				)
			}
		}
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

	if knownAndSet(config.NotifyOnRecovery) && config.NotifyOnRecovery.ValueBool() && config.IsRealTime.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("notify_on_recovery"),
			"Recovery notifications do not apply to a real-time alert",
			"A real-time alert keeps no state between records, so it never recovers and the server rejects "+
				"`notify_on_recovery = true` on one. Remove `notify_on_recovery`, or drop `is_real_time`.",
		)
	}

	validatePromQLRuleMode(&config, &resp.Diagnostics)

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

	wanted := plan
	if plan.QueryCondition != nil {
		qc := *plan.QueryCondition
		wanted.QueryCondition = &qc
	}
	r.refresh(ctx, org, alertID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	// A warning, not an error: an error on create taints the alert, and
	// every later apply would destroy and recreate it. The next plan shows
	// the setting as a change, and that update fails with the same message.
	reportSettingsNotKept(&wanted, &plan, &resp.Diagnostics, true)
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
	r.refreshFolder(ctx, org, alertID, &state, &resp.Diagnostics)
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

	var config AlertResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.keepUnmanagedSettings(ctx, org, alertID, &config, &body, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

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

	planned := plannedUnsetValuesOf(&plan)
	r.refresh(ctx, org, alertID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	planned.restore(&config, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	reportSettingsNotKept(&config, &plan, &resp.Diagnostics, false)
}

// plannedUnsetValues holds the planned values of the Optional+Computed
// attributes an update keeps from the server when they are unset.
//
// Unset in the configuration, the plan carries the value last read, and the
// write sends the server's current value, which differs only when it changed
// after the plan was made. State keeps the planned value then, as Terraform
// requires of a known planned value, and the next refresh reads the server's
// without a diff, because the attribute is computed and unset.
type plannedUnsetValues struct {
	notifyOnRecovery     types.Bool
	destinations         types.Set
	recoveryDestinations types.Set
	keepFiringFor        types.Int64
	suppressRepeats      types.Bool
	runbookURL           types.String
	oncallTeam           types.String
	operator             types.String
	threshold            types.Int64
	groupBy              *types.List
	promqlRuleMode       *types.Bool
}

func plannedUnsetValuesOf(plan *AlertResourceModel) plannedUnsetValues {
	p := plannedUnsetValues{
		notifyOnRecovery:     plan.NotifyOnRecovery,
		destinations:         plan.Destinations,
		recoveryDestinations: plan.RecoveryDestinations,
		keepFiringFor:        plan.KeepFiringFor,
		suppressRepeats:      plan.SuppressRepeats,
		runbookURL:           plan.RunbookURL,
		oncallTeam:           plan.OncallTeam,
		operator:             types.StringUnknown(),
		threshold:            types.Int64Unknown(),
	}
	if plan.QueryCondition != nil {
		ruleMode := plan.QueryCondition.PromQLRuleMode
		p.promqlRuleMode = &ruleMode
	}
	if plan.TriggerCondition != nil {
		p.operator = plan.TriggerCondition.Operator
		p.threshold = plan.TriggerCondition.Threshold
	}
	if plan.QueryCondition != nil && plan.QueryCondition.Aggregation != nil {
		groupBy := plan.QueryCondition.Aggregation.GroupBy
		p.groupBy = &groupBy
	}
	return p
}

func (p plannedUnsetValues) restore(config, model *AlertResourceModel) {
	if config.NotifyOnRecovery.IsNull() && !p.notifyOnRecovery.IsUnknown() {
		model.NotifyOnRecovery = p.notifyOnRecovery
	}
	if config.Destinations.IsNull() && !p.destinations.IsUnknown() {
		model.Destinations = p.destinations
	}
	if config.RecoveryDestinations.IsNull() && !p.recoveryDestinations.IsUnknown() {
		model.RecoveryDestinations = p.recoveryDestinations
	}
	if config.KeepFiringFor.IsNull() && !p.keepFiringFor.IsUnknown() {
		model.KeepFiringFor = p.keepFiringFor
	}
	if config.SuppressRepeats.IsNull() && !p.suppressRepeats.IsUnknown() {
		model.SuppressRepeats = p.suppressRepeats
	}
	if config.RunbookURL.IsNull() && !p.runbookURL.IsUnknown() {
		model.RunbookURL = p.runbookURL
	}
	if config.OncallTeam.IsNull() && !p.oncallTeam.IsUnknown() {
		model.OncallTeam = p.oncallTeam
	}
	if p.promqlRuleMode != nil && !p.promqlRuleMode.IsUnknown() &&
		config.QueryCondition != nil && config.QueryCondition.PromQLRuleMode.IsNull() &&
		model.QueryCondition != nil {
		model.QueryCondition.PromQLRuleMode = *p.promqlRuleMode
	}
	if config.TriggerCondition != nil && model.TriggerCondition != nil {
		if config.TriggerCondition.Operator.IsNull() && !p.operator.IsUnknown() {
			model.TriggerCondition.Operator = p.operator
		}
		if config.TriggerCondition.Threshold.IsNull() && !p.threshold.IsUnknown() {
			model.TriggerCondition.Threshold = p.threshold
		}
	}
	if p.groupBy != nil && !p.groupBy.IsUnknown() &&
		config.QueryCondition != nil && config.QueryCondition.Aggregation != nil &&
		config.QueryCondition.Aggregation.GroupBy.IsNull() &&
		model.QueryCondition != nil && model.QueryCondition.Aggregation != nil {
		model.QueryCondition.Aggregation.GroupBy = *p.groupBy
	}
}

// keepUnmanagedSettings fills in what an update sends for the settings the
// configuration does not decide.
//
// The update endpoint replaces the whole alert, so any field the body leaves
// out is reset. A configured setting is sent as written. An unset one
// (notify_on_recovery, recovery_destinations, keep_firing_for,
// suppress_repeats, runbook_url, oncall_team, promql_rule_mode, and the
// Optional+Computed attributes of keepServerComputed) is read from the server
// just before the write, and so is incident_title_template, which has no
// attribute yet. State is not used for this:
// it may be stale (a plan made with -refresh=false) or written by a release
// that did not know the attribute.
//
// A real-time alert can carry no recovery settings, and the server rejects an
// alert that does. Unset recovery_destinations, keep_firing_for and
// suppress_repeats are dropped for one: the user asked for a real-time alert
// and did not ask to keep them. notify_on_recovery is never dropped silently:
// when the server holds true and the configuration does not say, the update
// stops and asks for an explicit `false`.
func (r *AlertResource) keepUnmanagedSettings(ctx context.Context, org, alertID string, config *AlertResourceModel, body *AlertAPI, diags *diag.Diagnostics) {
	current, err := r.client.GetAlert(ctx, org, alertID)
	if err != nil {
		diags.AddError("Error reading alert before update",
			"The update replaces the whole alert, so the provider reads the settings it does not manage, "+
				"to send them back, and that read failed: "+err.Error())
		return
	}
	if current == nil {
		current = &AlertAPI{}
	}
	keepServerComputed(config, current, body)

	if config.RunbookURL.IsNull() {
		body.RunbookURL = current.RunbookURL
	}
	if config.OncallTeam.IsNull() {
		body.OncallTeam = current.OncallTeam
	}
	// incident_title_template has no attribute yet, because no released server
	// keeps it. It is sent back as the server holds it.
	body.IncidentTitleTemplate = current.IncidentTitleTemplate
	// Rule mode only means something on a PromQL alert, and the server ignores
	// it on any other, so an unset one is kept only while the alert stays
	// PromQL.
	if config.QueryCondition == nil || config.QueryCondition.PromQLRuleMode.IsNull() {
		body.QueryCondition.PromQLRuleMode = false
		if body.QueryCondition.QueryType == "promql" && current.QueryCondition.QueryType == "promql" {
			body.QueryCondition.PromQLRuleMode = current.QueryCondition.PromQLRuleMode
		}
	}

	configured := config.NotifyOnRecovery
	notify := current.NotifyOnRecovery != nil && *current.NotifyOnRecovery
	if !configured.IsNull() && !configured.IsUnknown() {
		notify = configured.ValueBool()
	} else if notify && body.IsRealTime {
		diags.AddAttributeError(
			path.Root("notify_on_recovery"),
			"Recovery notifications are on for this alert",
			"The alert has notify_on_recovery switched on, which a real-time alert cannot have. The provider "+
				"does not turn it off unasked: set `notify_on_recovery = false` to make this a real-time alert.",
		)
		return
	}
	body.NotifyOnRecovery = &notify

	if body.IsRealTime {
		if config.KeepFiringFor.IsNull() {
			body.KeepFiringFor = 0
		}
		if config.SuppressRepeats.IsNull() {
			body.SuppressRepeats = false
		}
		if config.RecoveryDestinations.IsNull() {
			body.RecoveryDestinations = nil
		}
		return
	}
	if config.KeepFiringFor.IsNull() {
		body.KeepFiringFor = current.KeepFiringFor
	}
	if config.SuppressRepeats.IsNull() {
		body.SuppressRepeats = current.SuppressRepeats
	}
	// The server refuses recovery destinations without the switch, so turning
	// recovery off in the configuration drops unset ones along with it.
	if config.RecoveryDestinations.IsNull() {
		body.RecoveryDestinations = nil
		if notify {
			body.RecoveryDestinations = current.RecoveryDestinations
		}
	}
}

// keepServerComputed sends the server's value for the Optional+Computed
// attributes the configuration leaves unset: destinations, the trigger's
// operator and threshold, and the aggregation's group_by. Sending what the plan
// holds instead would send nothing for a value that is unknown, which clears
// it, or a stale value from state.
//
// The trigger defaults depend on the alert family: an SLO alert has no count
// gate, and the server rejects a non-default one. When the family changes, the
// server's operator and threshold belong to the old family, so the new
// family's defaults are sent instead.
func keepServerComputed(config *AlertResourceModel, current, body *AlertAPI) {
	if config.Destinations.IsNull() {
		body.Destinations = current.Destinations
		if body.Destinations == nil {
			body.Destinations = []string{}
		}
	}

	if config.TriggerCondition != nil {
		isSlo := body.QueryCondition.QueryType == "slo"
		sameFamily := isSlo == (current.QueryCondition.QueryType == "slo")
		if config.TriggerCondition.Operator.IsNull() {
			switch {
			case sameFamily && current.TriggerCondition.Operator != "":
				body.TriggerCondition.Operator = current.TriggerCondition.Operator
			case isSlo:
				body.TriggerCondition.Operator = "="
			default:
				body.TriggerCondition.Operator = ">="
			}
		}
		if config.TriggerCondition.Threshold.IsNull() {
			switch {
			case sameFamily && current.TriggerCondition.Operator != "":
				body.TriggerCondition.Threshold = current.TriggerCondition.Threshold
			case isSlo:
				body.TriggerCondition.Threshold = 0
			default:
				body.TriggerCondition.Threshold = 1
			}
		}
	}

	if config.QueryCondition != nil && config.QueryCondition.Aggregation != nil &&
		config.QueryCondition.Aggregation.GroupBy.IsNull() &&
		body.QueryCondition.Aggregation != nil && current.QueryCondition.Aggregation != nil {
		body.QueryCondition.Aggregation.GroupBy = current.QueryCondition.Aggregation.GroupBy
	}
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

// ImportState supports:
//
//	terraform import openobserve_alert.example default/2abcXYZ
//	terraform import openobserve_alert.example default/my_folder/2abcXYZ
//
// The folder in the three-part form is only where the lookup starts; the
// folder the server reports wins.
func (r *AlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	var org, folderID, alertID string
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		org, folderID, alertID = parts[0], "default", parts[1]
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		org, folderID, alertID = parts[0], parts[1], parts[2]
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{alert_id}` or `{org_id}/{folder_id}/{alert_id}`, "+
				"for example `default/2abcXYZ` or `default/my_folder/2abcXYZ`.",
		)
		return
	}

	alert, err := r.client.GetAlert(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert during import", err.Error())
		return
	}
	if alert == nil {
		resp.Diagnostics.AddError("Alert not found", fmt.Sprintf("Alert %q not found in org %q.", alertID, org))
		return
	}

	state := AlertResourceModel{
		ID:       types.StringValue(alertResourceID(org, alertID)),
		OrgID:    types.StringValue(org),
		AlertID:  types.StringValue(alertID),
		FolderID: types.StringValue(folderID),
	}
	r.applyAlertToModel(ctx, alert, &state, &resp.Diagnostics)
	r.refreshFolder(ctx, org, alertID, &state, &resp.Diagnostics)
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
		NotifyOnRecovery: optBool(model.NotifyOnRecovery),
		Tags:             stringsFromSet(ctx, model.Tags, diags),
		// Unset, these send the zero value here; an update replaces it with
		// the server's in keepUnmanagedSettings, and a create takes the
		// server default, which is the zero value.
		RecoveryDestinations: stringsFromSet(ctx, model.RecoveryDestinations, diags),
		KeepFiringFor:        model.KeepFiringFor.ValueInt64(),
		SuppressRepeats:      model.SuppressRepeats.ValueBool(),
		RunbookURL:           clearableString(model.RunbookURL),
		OncallTeam:           clearableString(model.OncallTeam),
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
		PromQLRuleMode:     model.PromQLRuleMode.ValueBool(),
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

// canonicalConditionOperators maps every accepted spelling of a word-shaped
// operator onto one form, for comparison only.
//
// The API request model declares the operator enum with no serde rename, so a
// write must use PascalCase for the typed conditions. The storage model behind
// the untagged V1 `ConditionList` uses snake_case, and the `conditions`
// document is parsed and re-serialized through it. Both spellings are accepted
// on write there, but everything comes back snake_case.
var canonicalConditionOperators = map[string]string{
	"Contains": "contains", "contains": "contains",
	"NotContains": "not_contains", "not_contains": "not_contains",
	"IsNull": "is_null", "is_null": "is_null",
	"IsNotNull": "is_not_null", "is_not_null": "is_not_null",
	"IsEmpty": "is_empty", "is_empty": "is_empty",
	"IsNotEmpty": "is_not_empty", "is_not_empty": "is_not_empty",
}

// normalizeConditionOperators rewrites every `operator` in a conditions
// document to one spelling, so a comparison does not mistake the server's
// casing for drift.
//
// It recurses because conditions nest, and not uniformly: `or` and `and` hold
// arrays whose entries may themselves be `or`/`and` objects, while `not` holds
// a single object. Walking maps and slices generically covers all three
// without encoding the grammar.
func normalizeConditionOperators(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "operator" {
				if s, ok := val.(string); ok {
					if canonical, known := canonicalConditionOperators[s]; known {
						out[k] = canonical
						continue
					}
				}
			}
			out[k] = normalizeConditionOperators(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeConditionOperators(t[i])
		}
		return out
	default:
		return v
	}
}

// conditionsToModel renders a conditions document back, keeping the configured
// spelling of any operator the server rewrote. It serves both alerts and a
// pipeline's condition nodes, which carry the same document.
//
// Writing the server's spelling into state where the configuration said
// something else is an inconsistent result after apply, which fails the apply
// outright, and reports drift on every plan after that.
//
// It also accepts the one restructuring that changes nothing: a bare array of
// conditions (the legacy v1 list) is read back as `{"and": [...]}`. The
// server evaluates a legacy list as a conjunction and serializes it that way
// on purpose (`serialize_legacy_conditions`), so both forms mean the same
// filter, and storing the server's form would fail every apply.
//
// Other restructuring is not presentational: unknown keys are dropped, and a
// document with both a top-level `or` and a top-level `and` silently loses the
// `and`. Those are deliberately left to surface as a mismatch rather than be
// papered over.
func conditionsToModel(prior types.String, raw json.RawMessage, diags *diag.Diagnostics) types.String {
	return reconcileJSONWith(prior, raw, normalizeConditions, diags)
}

// normalizeConditions puts a conditions document in a comparable form: legacy
// lists become the `and` node the server reads them back as, and operators
// take one spelling.
func normalizeConditions(v any) any {
	return normalizeConditionOperators(canonicalConditionList(v))
}

// canonicalConditionList rewrites every legacy list (a bare JSON array where a
// condition list is expected) as `{"and": [...]}`, mirroring the server's
// serializer. A condition list is the document itself, each entry of an `or`
// or `and` array, and the value of `not`; a legacy list's own entries are
// plain conditions and are left alone.
func canonicalConditionList(v any) any {
	switch t := v.(type) {
	case []any:
		return map[string]any{"and": t}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "or", "and":
				if entries, ok := val.([]any); ok {
					children := make([]any, len(entries))
					for i := range entries {
						children[i] = canonicalConditionList(entries[i])
					}
					out[k] = children
					continue
				}
			case "not":
				out[k] = canonicalConditionList(val)
				continue
			}
			out[k] = val
		}
		return out
	default:
		return v
	}
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

// refreshFolder resolves which folder holds the alert, starting with the folder
// already in the model so an unmoved alert costs one list call. When the alert
// is in no folder the caller can list, the model keeps its folder and a warning
// says so, rather than reporting a folder change nobody made.
func (r *AlertResource) refreshFolder(ctx context.Context, org, alertID string, model *AlertResourceModel, diags *diag.Diagnostics) {
	known := model.FolderID.ValueString()
	folderID, err := r.client.FindAlertFolder(ctx, org, alertID, known)
	if err != nil {
		diags.AddWarning(
			"Could not resolve alert folder",
			fmt.Sprintf("Looking up which folder holds alert %q in org %q failed, so folder_id stays %q: %s",
				alertID, org, known, err.Error()),
		)
		return
	}
	if folderID == "" {
		diags.AddWarning(
			"Alert folder not found",
			fmt.Sprintf("Alert %q was not found in any alert folder of org %q that these credentials can list, "+
				"so folder_id stays %q (the value in state, or the import ID's folder, `default` when it names "+
				"none) and a move of the alert to another folder cannot be detected. Grant the provider's user "+
				"read access to the alert's folder.", alertID, org, known),
		)
		return
	}
	model.FolderID = types.StringValue(folderID)
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
	model.NotifyOnRecovery = types.BoolValue(alert.NotifyOnRecovery != nil && *alert.NotifyOnRecovery)
	model.RecoveryDestinations = setFromStrings(ctx, alert.RecoveryDestinations, diags)
	model.KeepFiringFor = types.Int64Value(alert.KeepFiringFor)
	model.SuppressRepeats = types.BoolValue(alert.SuppressRepeats)
	model.RunbookURL = clearedStringToModel(model.RunbookURL, alert.RunbookURL)
	model.OncallTeam = clearedStringToModel(model.OncallTeam, alert.OncallTeam)
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
	var priorConditions types.String
	if prior != nil {
		priorConditions = prior.Conditions
	}

	out := &AlertQueryConditionModel{
		QueryType:          types.StringValue(api.QueryType),
		SQL:                stringFromPtr(api.SQL),
		PromQL:             stringFromPtr(api.PromQL),
		PromQLWarningValue: float64FromPtr(api.PromQLWarningValue),
		PromQLMultiAlert:   boolPreserveNull(priorPromQLMultiAlert, api.PromQLMultiAlert),
		PromQLRuleMode:     types.BoolValue(api.PromQLRuleMode),
		Conditions:         conditionsToModel(priorConditions, api.Conditions, diags),
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

// keepServerValueDoc ends the description of every alert setting that keeps
// the server's value while unset.
const keepServerValueDoc = "Leave it unset to keep whatever the server holds across updates, so a value set in " +
	"the UI or through the API is not reset by an unrelated change. Set it to manage the value, and a change " +
	"made elsewhere then shows as drift. Removing it from a configuration that set it stops managing it and " +
	"leaves the server value as it is."

// clearableString is what a write sends for an alert string setting that
// `""` clears. The server refuses an empty runbook_url and treats an empty
// on-call team as none, so "none" is sent as an absent field.
func clearableString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	s := v.ValueString()
	return &s
}

// clearedStringToModel reads back a setting written by clearableString. The
// server reports a cleared setting as absent, which stays `""` when the model
// already held `""`, the value a configuration uses to clear it; otherwise
// the plan would show a change from "" to null on every run.
func clearedStringToModel(prior types.String, server *string) types.String {
	if server == nil || *server == "" {
		if !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
			return types.StringValue("")
		}
		return types.StringNull()
	}
	return types.StringValue(*server)
}

// runbookURLValidator applies the server's runbook link rules at plan time:
// an http or https URL with a host, no whitespace, at most 2048 characters.
// `""` passes, because it clears the link.
type runbookURLValidator struct{}

func (runbookURLValidator) Description(_ context.Context) string {
	return "must be an http:// or https:// URL with a host, or empty"
}

func (v runbookURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (runbookURLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || req.ConfigValue.ValueString() == "" {
		return
	}
	if problem := runbookURLProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid runbook_url",
			fmt.Sprintf("%s. The server refuses a runbook link that is not an http:// or https:// URL with a host.", problem))
	}
}

// runbookURLProblem mirrors the server's normalize_runbook_url and returns
// why raw is refused, or "" when it is accepted. Unlike the server it does
// not trim: a value with leading or trailing spaces is stored as sent, and the
// server's check would pass it, but it is almost certainly a paste accident.
func runbookURLProblem(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "runbook_url is blank"
	}
	if len([]rune(raw)) > 2048 {
		return "runbook_url is longer than 2048 characters"
	}
	if strings.ContainsFunc(raw, unicode.IsSpace) {
		return "runbook_url contains whitespace"
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || (!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https")) {
		return "runbook_url must start with http:// or https://"
	}
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if host == "" {
		return "runbook_url has no host"
	}
	return ""
}

// validateRecoverySettings catches the recovery settings the server refuses:
// any of them on a real-time alert, which keeps no state to recover from,
// recovery destinations with recovery switched off, and keep_firing_for
// outside 0 to one day.
func validateRecoverySettings(config *AlertResourceModel, diags *diag.Diagnostics) {
	realTime := knownAndSet(config.IsRealTime) && config.IsRealTime.ValueBool()
	if knownAndSet(config.KeepFiringFor) {
		secs := config.KeepFiringFor.ValueInt64()
		if secs < 0 || secs > keepFiringForMaxSecs {
			diags.AddAttributeError(path.Root("keep_firing_for"), "keep_firing_for out of range",
				fmt.Sprintf("`keep_firing_for` is %d. It is in seconds and must be between 0 and %d (one day).",
					secs, keepFiringForMaxSecs))
		} else if secs != 0 && realTime {
			diags.AddAttributeError(path.Root("keep_firing_for"), "keep_firing_for does not apply to a real-time alert",
				"A real-time alert keeps no state between records, so it never recovers and the server rejects "+
					"a non-zero `keep_firing_for` on one. Remove it, or drop `is_real_time`.")
		}
	}
	if knownAndSet(config.SuppressRepeats) && config.SuppressRepeats.ValueBool() && realTime {
		diags.AddAttributeError(path.Root("suppress_repeats"), "suppress_repeats does not apply to a real-time alert",
			"A real-time alert keeps no state, so it has no last notification to compare against and the server "+
				"rejects `suppress_repeats = true` on one. Remove it, or drop `is_real_time`.")
	}
	if knownAndSet(config.RecoveryDestinations) && len(config.RecoveryDestinations.Elements()) > 0 {
		switch {
		case realTime:
			diags.AddAttributeError(path.Root("recovery_destinations"),
				"recovery_destinations does not apply to a real-time alert",
				"A real-time alert never recovers, so it sends no recovery notification. Remove "+
					"`recovery_destinations`, or drop `is_real_time`.")
		case knownAndSet(config.NotifyOnRecovery) && !config.NotifyOnRecovery.ValueBool():
			diags.AddAttributeError(path.Root("recovery_destinations"), "recovery_destinations needs notify_on_recovery",
				"`notify_on_recovery` is `false`, so no recovery notification is sent and the server refuses "+
					"recovery destinations. Set `notify_on_recovery = true`, or remove `recovery_destinations`.")
		}
	}
}

// keepFiringForMaxSecs is the server's ceiling on keep_firing_for.
const keepFiringForMaxSecs = 24 * 60 * 60

// validatePromQLRuleMode catches rule mode where the server refuses it, and
// the combinations the server rewrites on save, which would otherwise fail the
// apply with "Provider produced inconsistent result".
func validatePromQLRuleMode(config *AlertResourceModel, diags *diag.Diagnostics) {
	qc := config.QueryCondition
	if qc == nil || !knownAndSet(qc.PromQLRuleMode) || !qc.PromQLRuleMode.ValueBool() {
		return
	}
	attr := path.Root("query_condition").AtName("promql_rule_mode")
	if !qc.QueryType.IsUnknown() && qc.QueryType.ValueString() != "promql" {
		diags.AddAttributeError(attr, "Rule mode needs a PromQL alert",
			"`promql_rule_mode` evaluates a PromQL expression, so it needs `query_condition.type = \"promql\"`.")
		return
	}
	if knownAndSet(config.StreamType) && config.StreamType.ValueString() != "metrics" {
		diags.AddAttributeError(attr, "Rule mode needs a metrics stream",
			"The server refuses `promql_rule_mode` unless `stream_type` is `metrics`.")
	}
	if knownAndSet(config.PendingPeriodSec) && config.PendingPeriodSec.ValueInt64() > 0 &&
		!(knownAndSet(qc.PromQLMultiAlert) && qc.PromQLMultiAlert.ValueBool()) && !qc.PromQLMultiAlert.IsUnknown() {
		diags.AddAttributeError(path.Root("query_condition").AtName("promql_multi_alert"),
			"Rule mode with a pending period is a multi-alert",
			"With `promql_rule_mode` and a `pending_period_sec`, the server turns on `promql_multi_alert` so "+
				"that each series waits out its own pending period. Set `promql_multi_alert = true` to match.")
	}
	// Every returned series is firing, so the threshold lives in the
	// expression and the server refuses anything else that would compare.
	conflicts := []struct {
		set  bool
		path path.Path
		name string
	}{
		{qc.PromQLCondition != nil, path.Root("query_condition").AtName("promql_condition"), "`promql_condition`"},
		{knownAndSet(qc.PromQLWarningValue), path.Root("query_condition").AtName("promql_warning_value"), "`promql_warning_value`"},
		{qc.Aggregation != nil, path.Root("query_condition").AtName("aggregation"), "`aggregation`"},
		{knownAndSet(qc.MultiTimeRange) && len(qc.MultiTimeRange.Elements()) > 0,
			path.Root("query_condition").AtName("multi_time_range"), "`multi_time_range`"},
		{config.TriggerCondition != nil && knownAndSet(config.TriggerCondition.WarningThreshold),
			path.Root("trigger_condition").AtName("warning_threshold"), "`warning_threshold`"},
	}
	for _, c := range conflicts {
		if c.set {
			diags.AddAttributeError(c.path, "Not accepted in rule mode",
				fmt.Sprintf("In `promql_rule_mode` every series the expression returns is firing, so the "+
					"comparison belongs in `promql` itself, for example `rate(errors[5m]) > 0.1`. The server "+
					"refuses %s alongside it. Remove it, or turn rule mode off.", c.name))
		}
	}
	if knownAndSet(config.PendingPeriodSec) && config.PendingPeriodSec.ValueInt64() > 0 &&
		knownAndSet(config.CreatesIncident) && config.CreatesIncident.ValueBool() {
		diags.AddAttributeError(path.Root("creates_incident"), "Rule mode with a pending period cannot open incidents",
			"With `promql_rule_mode` and a `pending_period_sec` the alert is a per-series multi-alert, and the "+
				"server refuses `creates_incident = true` on a multi-alert. Remove one of the three.")
	}
	// The provider fills in `>=` and `1` for an unset operator and threshold,
	// so a partly configured trigger is checked with those.
	tc := config.TriggerCondition
	if tc == nil || tc.Operator.IsUnknown() || tc.Threshold.IsUnknown() || (tc.Operator.IsNull() && tc.Threshold.IsNull()) {
		return
	}
	op, threshold := ">=", int64(1)
	if !tc.Operator.IsNull() {
		op = tc.Operator.ValueString()
	}
	if !tc.Threshold.IsNull() {
		threshold = tc.Threshold.ValueInt64()
	}
	switch {
	case (op == ">=" && threshold == 1) || (op == ">" && threshold == 0):
	case op == "=" && threshold == 0:
		diags.AddAttributeError(path.Root("trigger_condition").AtName("threshold"),
			"Rule mode fires on any returned series",
			"In rule mode every series the expression returns is firing, and the server replaces an "+
				"`= 0` trigger with `>= 1`. Remove `operator` and `threshold`, or set them to `>=` and `1`.")
	default:
		diags.AddAttributeError(path.Root("trigger_condition").AtName("threshold"),
			"Rule mode fires on any returned series",
			fmt.Sprintf("The trigger is `%s %d` (an unset operator is `>=` and an unset threshold `1`), but in "+
				"rule mode the alert fires when any series is returned, and the server accepts only `>= 1` or "+
				"`> 0`. Remove `operator` and `threshold` to get `>= 1`.", op, threshold))
	}
}

// reportSettingsNotKept reports a write in which the server did not keep a
// setting the configuration sets: an error, or a warning when warnOnly. A server older than the setting drops a field it
// does not know without an error, and Terraform would otherwise report the
// difference as a provider bug ("inconsistent result after apply"). State
// already holds what the server kept, so the next plan shows the difference.
func reportSettingsNotKept(want, got *AlertResourceModel, diags *diag.Diagnostics, warnOnly bool) {
	type setting struct {
		path    path.Path
		want    attr.Value
		got     attr.Value
		release string
	}
	settings := []setting{
		{path.Root("recovery_destinations"), want.RecoveryDestinations, got.RecoveryDestinations, "v1.1.0"},
		{path.Root("keep_firing_for"), want.KeepFiringFor, got.KeepFiringFor, "v1.1.0"},
		{path.Root("suppress_repeats"), want.SuppressRepeats, got.SuppressRepeats, "v1.2.0-rc1"},
		{path.Root("runbook_url"), want.RunbookURL, got.RunbookURL, "v1.1.0"},
		{path.Root("oncall_team"), want.OncallTeam, got.OncallTeam, "v1.1.0"},
	}
	if want.QueryCondition != nil && got.QueryCondition != nil {
		settings = append(settings, setting{path.Root("query_condition").AtName("promql_rule_mode"),
			want.QueryCondition.PromQLRuleMode, got.QueryCondition.PromQLRuleMode, "v1.2.0-rc1"})
	}
	for _, s := range settings {
		if !knownAndSet(s.want) || s.want.Equal(s.got) {
			continue
		}
		if w, ok := s.want.(types.String); ok && w.ValueString() == "" && s.got.IsNull() {
			continue
		}
		detail := fmt.Sprintf("The alert was saved, but the server reports %s where the configuration sets %s. "+
			"OpenObserve servers older than the setting ignore it: this one needs OpenObserve %s or "+
			"newer. Upgrade the server, or remove the attribute from the configuration.",
			s.got, s.want, s.release)
		if warnOnly {
			diags.AddAttributeWarning(s.path, "The server did not keep this setting", detail)
		} else {
			diags.AddAttributeError(s.path, "The server did not keep this setting", detail)
		}
	}
}

// clearableTrimmedValidator accepts "" (clear) or a value with no leading or
// trailing whitespace. The server treats a blank value as none, which the
// provider would otherwise report as a setting the server did not keep.
type clearableTrimmedValidator struct{}

func (clearableTrimmedValidator) Description(context.Context) string {
	return "must be empty, or have no leading or trailing whitespace"
}

func (v clearableTrimmedValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (clearableTrimmedValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !knownAndSet(req.ConfigValue) {
		return
	}
	if v := req.ConfigValue.ValueString(); v != strings.TrimSpace(v) {
		resp.Diagnostics.AddAttributeError(req.Path, "Leading or trailing whitespace",
			fmt.Sprintf("%q has leading or trailing whitespace. Use \"\" to clear the setting.", v))
	}
}

// ModifyPlan catches what depends on the server's current values or on
// whether the alert is new, which ValidateConfig cannot see.
func (r *AlertResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, config AlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A new alert takes the server default for an unset notify_on_recovery,
	// which is false, and the server refuses recovery destinations without it.
	if req.State.Raw.IsNull() && config.NotifyOnRecovery.IsNull() &&
		knownAndSet(config.RecoveryDestinations) && len(config.RecoveryDestinations.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("recovery_destinations"),
			"recovery_destinations needs notify_on_recovery",
			"A new alert starts with `notify_on_recovery = false`, and the server refuses recovery destinations "+
				"without it. Set `notify_on_recovery = true`.")
	}

	// Rule mode held by the server, with the attribute unset: the server turns
	// on promql_multi_alert when there is a pending period, which an unset
	// promql_multi_alert cannot plan. ValidateConfig covers a configured
	// rule mode.
	if config.QueryCondition == nil || !config.QueryCondition.PromQLRuleMode.IsNull() || plan.QueryCondition == nil {
		return
	}
	pq := plan.QueryCondition
	if !knownAndSet(pq.PromQLRuleMode) || !pq.PromQLRuleMode.ValueBool() ||
		pq.QueryType.ValueString() != "promql" ||
		!knownAndSet(plan.PendingPeriodSec) || plan.PendingPeriodSec.ValueInt64() == 0 ||
		pq.PromQLMultiAlert.IsUnknown() || (knownAndSet(pq.PromQLMultiAlert) && pq.PromQLMultiAlert.ValueBool()) {
		return
	}
	resp.Diagnostics.AddAttributeError(path.Root("query_condition").AtName("promql_multi_alert"),
		"Rule mode with a pending period is a multi-alert",
		"This alert has `promql_rule_mode` switched on in OpenObserve, and with a `pending_period_sec` the server "+
			"turns on `promql_multi_alert` so that each series waits out its own pending period. Set "+
			"`query_condition.promql_multi_alert = true`, or set `promql_rule_mode = false`.")
}
