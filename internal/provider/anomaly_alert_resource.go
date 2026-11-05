package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &AnomalyAlertResource{}
	_ resource.ResourceWithConfigure      = &AnomalyAlertResource{}
	_ resource.ResourceWithImportState    = &AnomalyAlertResource{}
	_ resource.ResourceWithValidateConfig = &AnomalyAlertResource{}
)

// anomalyMinVersion is the first OpenObserve release whose anomaly alerts carry
// the band settings (band width, direction and window share).
const anomalyMinVersion = "1.1.0"

// anomalyDetectionFunctions are the aggregations the trainer can read. The
// server matches them case-sensitively.
var anomalyDetectionFunctions = []string{"count", "avg", "sum", "min", "max", "p50", "p95", "p99"}

// anomalyDirections are the values of alert_direction.
var anomalyDirections = []string{"both", "above", "below"}

// anomalyIntervalPattern is the server's interval grammar: a number and one of
// s, m, h or d.
var anomalyIntervalPattern = regexp.MustCompile(`^[0-9]+[smhd]$`)

// anomalyFilterAttrTypes is the element type of the filters list.
var anomalyFilterAttrTypes = map[string]attr.Type{
	"field":    types.StringType,
	"operator": types.StringType,
	"value":    types.StringType,
}

// NewAnomalyAlertResource returns a factory for the openobserve_anomaly_alert resource.
func NewAnomalyAlertResource() resource.Resource {
	return &AnomalyAlertResource{}
}

// AnomalyAlertResource manages an anomaly alert (an anomaly detection config).
type AnomalyAlertResource struct {
	client *Client
}

// AnomalyAlertResourceModel holds the Terraform state for an anomaly alert.
type AnomalyAlertResourceModel struct {
	ID                     types.String  `tfsdk:"id"`
	AnomalyID              types.String  `tfsdk:"anomaly_id"`
	OrgID                  types.String  `tfsdk:"org_id"`
	FolderID               types.String  `tfsdk:"folder_id"`
	Name                   types.String  `tfsdk:"name"`
	Description            types.String  `tfsdk:"description"`
	StreamName             types.String  `tfsdk:"stream_name"`
	StreamType             types.String  `tfsdk:"stream_type"`
	Filters                types.List    `tfsdk:"filters"`
	CustomSQL              types.String  `tfsdk:"custom_sql"`
	DetectionFunction      types.String  `tfsdk:"detection_function"`
	DetectionFunctionField types.String  `tfsdk:"detection_function_field"`
	HistogramInterval      types.String  `tfsdk:"histogram_interval"`
	ScheduleInterval       types.String  `tfsdk:"schedule_interval"`
	DetectionWindowSeconds types.Int64   `tfsdk:"detection_window_seconds"`
	TrainingWindowDays     types.Int64   `tfsdk:"training_window_days"`
	RetrainIntervalDays    types.Int64   `tfsdk:"retrain_interval_days"`
	AlertBudgetPerDay      types.Float64 `tfsdk:"alert_budget_per_day"`
	BandWidth              types.Float64 `tfsdk:"band_width"`
	AlertDirection         types.String  `tfsdk:"alert_direction"`
	AlertWindowBuckets     types.Int64   `tfsdk:"alert_window_buckets"`
	AlertWindowFirePct     types.Float64 `tfsdk:"alert_window_fire_pct"`
	AlertWindowRecoverPct  types.Float64 `tfsdk:"alert_window_recover_pct"`
	AlertEnabled           types.Bool    `tfsdk:"alert_enabled"`
	AlertDestinations      types.Set     `tfsdk:"alert_destinations"`
	Enabled                types.Bool    `tfsdk:"enabled"`
	Owner                  types.String  `tfsdk:"owner"`
	Priority               types.Int64   `tfsdk:"priority"`
	Tags                   types.Set     `tfsdk:"tags"`

	BandGrouping        types.String  `tfsdk:"band_grouping"`
	BandK               types.Float64 `tfsdk:"band_k"`
	TrainingDataStartUs types.Int64   `tfsdk:"training_data_start_us"`
	TrainingDataEndUs   types.Int64   `tfsdk:"training_data_end_us"`
	IsTrained           types.Bool    `tfsdk:"is_trained"`
	Status              types.String  `tfsdk:"status"`
}

// AnomalyFilterModel is one element of the filters list.
type AnomalyFilterModel struct {
	Field    types.String `tfsdk:"field"`
	Operator types.String `tfsdk:"operator"`
	Value    types.String `tfsdk:"value"`
}

func (r *AnomalyAlertResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_anomaly_alert"
}

func (r *AnomalyAlertResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an anomaly alert: a detector trained on a stream's history that flags each bucket whose " +
			"value leaves the expected band `expected ± k·σ`, and notifies alert destinations.\n\n" +
			"**Requires OpenObserve Enterprise v" + anomalyMinVersion + " or later.** Anomaly detection is not part of " +
			"the open-source edition; against an open-source server this resource fails with a diagnostic saying so.\n\n" +
			"The series is one aggregation (`detection_function`) per `histogram_interval` bucket, read either through " +
			"`filters` or through `custom_sql`. OpenObserve starts training as soon as an enabled alert is created, " +
			"reading at least 21 days of history, and retrains every `retrain_interval_days`. Training fills " +
			"`band_grouping`, `band_k` and the training span; refresh after training to see them.\n\n" +
			"The band is `expected ± k·σ`. Leave `band_width` unset to use the trained k (between 3 and 6), set it to " +
			"fix k, or set `alert_budget_per_day` to let OpenObserve move k to hold a number of alerts per day. " +
			"`band_width` and `alert_budget_per_day` cannot both be set. Changing `band_width`, `alert_direction` or " +
			"the window settings takes effect on the next detection run without retraining.\n\n" +
			"Anomaly alerts live in **alert folders**, so `folder_id` refers to an `openobserve_folder` with " +
			"`folder_type = \"alerts\"`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{anomaly_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"anomaly_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned anomaly alert identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the anomaly alert belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("default"),
				Description: "Alert folder holding this anomaly alert. Changing it moves the alert in place, through the " +
					"same server call as moving it in the UI. On OpenObserve up to at least v1.2.0-rc1 that call does " +
					"not move the alert's RBAC ownership record, so with RBAC enabled, folder-based permissions may still " +
					"follow the old folder. Recreate the alert instead if that matters.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Anomaly alert name.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What this anomaly alert watches.",
			},
			"stream_name": schema.StringAttribute{
				Required:      true,
				Description:   "Stream the detector reads. Changing it replaces the anomaly alert.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"stream_type": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("logs"),
				Description:   "Stream type: `logs`, `metrics`, or `traces`. Changing it replaces the anomaly alert.",
				Validators:    []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"filters": schema.ListNestedAttribute{
				Optional: true,
				Description: "Row filters, ANDed together, for the filters query mode. Omit both `filters` and " +
					"`custom_sql` to aggregate every row of the stream. Conflicts with `custom_sql`.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"field": schema.StringAttribute{
							Required:    true,
							Description: "Field to filter on.",
						},
						"operator": schema.StringAttribute{
							Required: true,
							Description: "Operator, for example `=`, `!=`, `>`, `<`, `contains`, `not_contains`, " +
								"`starts_with`, `ends_with`, `in`, `not_in`, `is_null`, `is_not_null`, `str_match`, " +
								"`re_match` or `re_not_match`.",
						},
						"value": schema.StringAttribute{
							Required: true,
							Description: "Value to compare against, as a string. `in` and `not_in` take a comma-separated list. " +
								"Ignored by `is_null` and `is_not_null`; use `\"\"`.",
						},
					},
				},
			},
			"custom_sql": schema.StringAttribute{
				Optional: true,
				Description: "SQL producing the series, for the custom SQL query mode. It must bucket with " +
					"`histogram(_timestamp, '<histogram_interval>')` and return one numeric value per bucket. " +
					"Conflicts with `filters`.",
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("filters")),
					stringvalidator.LengthAtLeast(1),
				},
			},
			"detection_function": schema.StringAttribute{
				Required: true,
				Description: "Aggregation computed per bucket: `count`, `avg`, `sum`, `min`, `max`, `p50`, `p95` or " +
					"`p99`. Every function except `count` needs `detection_function_field`.",
				Validators: []validator.String{stringvalidator.OneOf(anomalyDetectionFunctions...)},
			},
			"detection_function_field": schema.StringAttribute{
				Optional: true,
				Description: "Field the aggregation reads. With `filters` it is the stream field to aggregate; with `custom_sql` it " +
					"is the query's output column that carries the value, for example `value`. Required for every " +
					"`detection_function` except `count`, which it must not be set with.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"histogram_interval": schema.StringAttribute{
				Required: true,
				Description: "Bucket width of the series, and so the detection interval, as a number and a unit: " +
					"`30s`, `5m`, `1h` or `1d`. It must divide OpenObserve's bucketing origin, 2001-01-01T00:00:00Z " +
					"(978307200 seconds after the Unix epoch), so that every bucket starts on the same grid: `30s`, " +
					"`1m`, `5m`, `10m`, `15m`, `30m`, `90m`, `1h`, `2h`, `3h`, `4h`, `6h`, `12h` and `1d` qualify, while " +
					"`7m`, `11m`, `5h`, `7h`, `10h` and `2d` do not.",
				Validators: []validator.String{intervalValidator(), histogramOriginValidator{}},
			},
			"schedule_interval": schema.StringAttribute{
				Required: true,
				Description: "How often detection runs, in the same form as `histogram_interval`. It must be a whole " +
					"multiple of `histogram_interval`.",
				Validators: []validator.String{intervalValidator()},
			},
			"detection_window_seconds": schema.Int64Attribute{
				Required: true,
				Description: "Look-back of each detection run, in seconds. It must be at least `schedule_interval` " +
					"plus `histogram_interval`.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"training_window_days": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(28),
				Description: "Days of history training reads. Training always reads at least 21 days. Defaults to `28`.",
				Validators:  []validator.Int64{int64validator.Between(1, math.MaxInt32)},
			},
			"retrain_interval_days": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(7),
				Description: "Days between automatic retrains; `0` never retrains. Defaults to `7`.",
				Validators:  []validator.Int64{int64validator.Between(0, 36500)},
			},
			"alert_budget_per_day": schema.Float64Attribute{
				Optional: true,
				Description: "Target number of delivered alerts per day. OpenObserve moves k to hold it. Must be " +
					"greater than 0. Conflicts with `band_width`.",
			},
			"band_width": schema.Float64Attribute{
				Optional: true,
				Description: "Band half-width k in standard deviations, between 1 and 10: the band is " +
					"`expected ± band_width·σ`. Leave unset (Auto) to use the trained k, `band_k`. Changing it needs " +
					"no retrain. Conflicts with `alert_budget_per_day`.",
				Validators: []validator.Float64{
					float64validator.Between(1, 10),
					float64validator.ConflictsWith(path.MatchRoot("alert_budget_per_day")),
				},
			},
			"alert_direction": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("both"),
				Description: "Which excursions are delivered: `both`, `above` (value over the band) or `below`. " +
					"Gates delivery only; detection still records both. Defaults to `both`.",
				Validators: []validator.String{stringvalidator.OneOf(anomalyDirections...)},
			},
			"alert_window_buckets": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(1),
				Description: "Length of the alert window in buckets, at least 1. The window times " +
					"`histogram_interval` must span at most 24 hours, which the server checks. Defaults to `1`.",
				Validators: []validator.Int64{int64validator.Between(1, math.MaxInt32)},
			},
			"alert_window_fire_pct": schema.Float64Attribute{
				Optional: true,
				Computed: true,
				Default:  float64default.StaticFloat64(100),
				Description: "Percent of the window's buckets that must be out of band to fire, greater than 0 and " +
					"at most 100. Defaults to `100`.",
			},
			"alert_window_recover_pct": schema.Float64Attribute{
				Optional: true,
				Description: "Out-of-band percent of the window below which a fired alert recovers, greater than 0 " +
					"and at most `alert_window_fire_pct`. Unset means equal to `alert_window_fire_pct`.",
			},
			"alert_enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether detected anomalies are delivered to `alert_destinations`. Detection runs either way.",
			},
			"alert_destinations": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Names of the alert destinations notified when the alert fires.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the detector trains and runs. A disabled alert is neither trained nor evaluated.",
			},
			"owner": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Email of the alert owner. Defaults to the account the provider authenticates as.",
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{keepStateWhenUnset{}},
			},
			"priority": schema.Int64Attribute{
				Optional:    true,
				Description: "Triage priority from 1 (most urgent) to 5. Display metadata only.",
				Validators:  []validator.Int64{int64validator.Between(1, 5)},
			},
			"tags": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Selection tags, lowercase, for example `prod` or `team:checkout`.",
			},
			"band_grouping": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{keepStateWhenUnset{}},
				Description: "Slot grouping training chose: `weekend_hour` (weekday or weekend, by hour of day, from " +
					"3 or more days of data) or `global`. Alerts trained by older builds may report `hour_of_week` or " +
					"`hour_of_day` until they retrain. Null until trained.",
			},
			"band_k": schema.Float64Attribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Float64{keepStateWhenUnset{}},
				Description: "The model's k: the p99 training z-score, between 3 and 6, unless `alert_budget_per_day` " +
					"has moved it. `band_width` overrides it when set. Null until trained.",
			},
			"training_data_start_us": schema.Int64Attribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{keepStateWhenUnset{}},
				Description: "Start of the data the current model was trained on, in microseconds. Null until " +
					"trained, and may be null in non-primary super-cluster regions.",
			},
			"training_data_end_us": schema.Int64Attribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{keepStateWhenUnset{}},
				Description: "End of the data the current model was trained on, in microseconds. Null until " +
					"trained, and may be null in non-primary super-cluster regions.",
			},
			"is_trained": schema.BoolAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{keepStateWhenUnset{}},
				Description:   "Whether a trained model is in force.",
			},
			"status": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{keepStateWhenUnset{}},
				Description:   "Lifecycle status: `waiting`, `training`, `ready`, `failed` or `disabled`.",
			},
		},
	}
}

// intervalValidator checks the server's interval grammar.
func intervalValidator() validator.String {
	return stringvalidator.RegexMatches(anomalyIntervalPattern,
		"must be a whole number followed by s, m, h or d, for example `5m` or `1h`")
}

// histogramOriginSecs is the origin OpenObserve's histogram() buckets from,
// 2001-01-01T00:00:00Z. An interval that does not divide it puts indexed and
// unindexed runs on different grids, so the server rejects it.
const histogramOriginSecs = 978307200

// intervalSeconds parses the server's interval grammar; ok is false when it
// does not match or overflows.
func intervalSeconds(v string) (int64, bool) {
	if !anomalyIntervalPattern.MatchString(v) {
		return 0, false
	}
	unit := map[byte]int64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[v[len(v)-1]]
	n, err := strconv.ParseInt(v[:len(v)-1], 10, 64)
	if err != nil || n > math.MaxInt64/unit {
		return 0, false
	}
	return n * unit, true
}

// histogramOriginValidator rejects a histogram_interval that does not divide
// the bucketing origin, the rule the server applies on create and update.
type histogramOriginValidator struct{}

func (histogramOriginValidator) Description(context.Context) string {
	return "must divide the histogram origin 2001-01-01T00:00:00Z"
}

func (v histogramOriginValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (histogramOriginValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !knownAndSet(req.ConfigValue) {
		return
	}
	secs, ok := intervalSeconds(req.ConfigValue.ValueString())
	if !ok || secs <= 0 {
		return // the grammar validator reports these
	}
	if histogramOriginSecs%secs != 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "histogram_interval is off the bucket grid",
			fmt.Sprintf("%q (%d seconds) does not divide OpenObserve's bucketing origin, 2001-01-01T00:00:00Z "+
				"(978307200 seconds), so its buckets would not line up between runs and the server rejects it. "+
				"Use an interval such as 5m, 10m, 15m, 30m, 1h, 2h, 3h, 4h, 6h, 12h or 1d.",
				req.ConfigValue.ValueString(), secs))
	}
}

// ValidateConfig checks the cross-field rules that need no server state. The
// rules that need the interval in seconds (the 24-hour window cap, the
// schedule/histogram pairing and the look-back floor) are left to the API.
func (r *AnomalyAlertResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config AnomalyAlertResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateAnomalyConfig(ctx, &config, &resp.Diagnostics)
}

// validateAnomalyConfig holds the plan-time rules so tests can call them
// without building a request.
func validateAnomalyConfig(ctx context.Context, config *AnomalyAlertResourceModel, diags *diag.Diagnostics) {
	if knownAndSet(config.AlertBudgetPerDay) {
		budget := config.AlertBudgetPerDay.ValueFloat64()
		if !(budget > 0) || math.IsInf(budget, 0) {
			diags.AddAttributeError(
				path.Root("alert_budget_per_day"),
				"Invalid alert budget",
				fmt.Sprintf("`alert_budget_per_day` must be a finite number greater than 0, got %v.", budget),
			)
		}
	}

	fire := 100.0
	fireKnown := true
	if config.AlertWindowFirePct.IsUnknown() {
		fireKnown = false
	} else if !config.AlertWindowFirePct.IsNull() {
		fire = config.AlertWindowFirePct.ValueFloat64()
		if !(fire > 0 && fire <= 100) {
			diags.AddAttributeError(
				path.Root("alert_window_fire_pct"),
				"Invalid alert window fire percent",
				fmt.Sprintf("`alert_window_fire_pct` must be greater than 0 and at most 100, got %v.", fire),
			)
			fireKnown = false
		}
	}
	if knownAndSet(config.AlertWindowRecoverPct) {
		recoverPct := config.AlertWindowRecoverPct.ValueFloat64()
		switch {
		case !(recoverPct > 0):
			diags.AddAttributeError(
				path.Root("alert_window_recover_pct"),
				"Invalid alert window recover percent",
				fmt.Sprintf("`alert_window_recover_pct` must be greater than 0, got %v.", recoverPct),
			)
		case fireKnown && recoverPct > fire:
			diags.AddAttributeError(
				path.Root("alert_window_recover_pct"),
				"Recover percent above fire percent",
				fmt.Sprintf("`alert_window_recover_pct` (%v) must not exceed `alert_window_fire_pct` (%v): an alert "+
					"would recover while it is still above the level that fires it.", recoverPct, fire),
			)
		}
	}

	if knownAndSet(config.DetectionFunctionField) {
		field := config.DetectionFunctionField.ValueString()
		if field != strings.TrimSpace(field) {
			diags.AddAttributeError(
				path.Root("detection_function_field"),
				"detection_function_field has surrounding whitespace",
				fmt.Sprintf("OpenObserve stores %q as written but reads it back trimmed. Write it as %q.",
					field, strings.TrimSpace(field)),
			)
		}
	}

	if knownAndSet(config.DetectionFunction) && !config.DetectionFunctionField.IsUnknown() {
		fn := config.DetectionFunction.ValueString()
		hasField := !config.DetectionFunctionField.IsNull()
		switch {
		case fn == "count" && hasField:
			diags.AddAttributeError(
				path.Root("detection_function_field"),
				"detection_function_field not allowed with count",
				"`count` counts rows and reads no field. Remove `detection_function_field`.",
			)
		case fn != "count" && !hasField:
			diags.AddAttributeError(
				path.Root("detection_function_field"),
				"Missing detection_function_field",
				fmt.Sprintf("`detection_function = %q` aggregates a field, so `detection_function_field` is required.", fn),
			)
		}
	}

	if knownAndSet(config.Tags) {
		var tags []types.String
		diags.Append(config.Tags.ElementsAs(ctx, &tags, false)...)
		for _, t := range tags {
			if !knownAndSet(t) {
				continue
			}
			v := t.ValueString()
			if strings.TrimSpace(v) == "" {
				diags.AddAttributeError(path.Root("tags"), "Empty tag",
					"OpenObserve drops empty tags, so the tag would read back missing. Remove it.")
				continue
			}
			if v != strings.ToLower(strings.TrimSpace(v)) {
				diags.AddAttributeError(
					path.Root("tags"),
					"Tags must be lowercase and trimmed",
					fmt.Sprintf("OpenObserve stores tags lowercased and trimmed, so %q would read back changed. "+
						"Write it as %q.", v, strings.ToLower(strings.TrimSpace(v))),
				)
			}
		}
	}
}

func (r *AnomalyAlertResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *AnomalyAlertResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AnomalyAlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := anomalyWriteFromModel(ctx, &plan, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// A server older than the band settings accepts the request and drops
	// them. Its existing configs show that before anything is written; with no
	// config to judge by, the read-back below decides.
	known, supported, err := r.client.AnomalyBandSettingsSupported(ctx, org)
	if err != nil {
		summary, detail := anomalyErrorDiag("creating", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if known && !supported {
		addBandSettingsError(&resp.Diagnostics)
		return
	}

	anomalyID, err := r.client.CreateAnomalyConfig(ctx, org, body)
	if err != nil {
		summary, detail := anomalyErrorDiag("creating", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.AnomalyID = types.StringValue(anomalyID)
	plan.ID = types.StringValue(anomalyResourceID(org, anomalyID))

	cfg, err := r.client.GetAnomalyConfig(ctx, org, anomalyID)
	if err != nil || cfg == nil {
		// Track what was created, so the failed read-back does not orphan it.
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("anomaly_id"), plan.AnomalyID)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("org_id"), plan.OrgID)...)
		if err != nil {
			summary, detail := anomalyErrorDiag("reading", err)
			resp.Diagnostics.AddError(summary, detail)
		} else {
			resp.Diagnostics.AddError("Anomaly alert not found after create",
				fmt.Sprintf("Anomaly alert %q was not found in org %q after being created.", anomalyID, org))
		}
		return
	}
	if !cfg.HasBandSettings {
		// Remove what this server cannot store, and keep nothing in state:
		// a tainted resource would only be recreated into the same failure.
		if delErr := r.client.DeleteAnomalyConfig(ctx, org, anomalyID); delErr != nil {
			resp.Diagnostics.AddError("Error removing the anomaly alert after a failed create",
				fmt.Sprintf("Anomaly alert %q in org %q could not be deleted: %v", anomalyID, org, delErr))
		}
		addBandSettingsError(&resp.Diagnostics)
		return
	}

	applyAnomalyToModel(ctx, cfg, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AnomalyAlertResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AnomalyAlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	anomalyID := state.AnomalyID.ValueString()

	cfg, err := r.client.GetAnomalyConfig(ctx, org, anomalyID)
	if err != nil {
		summary, detail := anomalyErrorDiag("reading", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if cfg == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(anomalyResourceID(org, anomalyID))
	applyAnomalyToModel(ctx, cfg, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AnomalyAlertResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AnomalyAlertResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := anomalyUpdateBody(ctx, &plan, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	anomalyID := state.AnomalyID.ValueString()
	planned := plan

	if err := r.client.UpdateAnomalyConfig(ctx, org, anomalyID, body); err != nil {
		summary, detail := anomalyErrorDiag("updating", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.AnomalyID = types.StringValue(anomalyID)
	plan.ID = types.StringValue(anomalyResourceID(org, anomalyID))
	r.refresh(ctx, org, anomalyID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	keepPlannedTrainingFields(&planned, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// anomalyUpdateBody is the update request for a plan. An unchanged
// schedule_interval is left out, because the server resets the detection
// trigger to run immediately whenever the field is sent.
func anomalyUpdateBody(ctx context.Context, plan, state *AnomalyAlertResourceModel, diags *diag.Diagnostics) AnomalyConfigWriteAPI {
	body := anomalyWriteFromModel(ctx, plan, false, diags)
	if plan.ScheduleInterval.Equal(state.ScheduleInterval) {
		body.ScheduleInterval = ""
	}
	return body
}

// keepPlannedTrainingFields holds the training state (the band, the training
// span, status and is_trained) at the values the plan carried over from state.
// An update never writes them, but training runs in the background and may
// finish between plan and apply; reporting its result here would contradict
// the plan. The next refresh picks it up.
func keepPlannedTrainingFields(planned, got *AnomalyAlertResourceModel) {
	if !planned.BandGrouping.IsUnknown() {
		got.BandGrouping = planned.BandGrouping
	}
	if !planned.BandK.IsUnknown() {
		got.BandK = planned.BandK
	}
	if !planned.TrainingDataStartUs.IsUnknown() {
		got.TrainingDataStartUs = planned.TrainingDataStartUs
	}
	if !planned.TrainingDataEndUs.IsUnknown() {
		got.TrainingDataEndUs = planned.TrainingDataEndUs
	}
	if !planned.IsTrained.IsUnknown() {
		got.IsTrained = planned.IsTrained
	}
	if !planned.Status.IsUnknown() {
		got.Status = planned.Status
	}
}

// keepStateWhenUnset plans the prior value of an attribute the configuration
// leaves unset, null included. UseStateForUnknown skips a null prior value, so
// an alert imported without an owner would otherwise plan `owner` as unknown
// on every update, and an untrained alert its training results.
type keepStateWhenUnset struct{}

func (keepStateWhenUnset) Description(context.Context) string {
	return "Keeps the prior value, even null, when the configuration does not set one."
}

func (m keepStateWhenUnset) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepStateWhenUnset) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepStateWhenUnset) PlanModifyFloat64(_ context.Context, req planmodifier.Float64Request, resp *planmodifier.Float64Response) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepStateWhenUnset) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepStateWhenUnset) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (r *AnomalyAlertResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AnomalyAlertResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAnomalyConfig(ctx, org, state.AnomalyID.ValueString()); err != nil {
		summary, detail := anomalyErrorDiag("deleting", err)
		resp.Diagnostics.AddError(summary, detail)
	}
}

// ImportState supports: terraform import openobserve_anomaly_alert.example default/34hd...
func (r *AnomalyAlertResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{anomaly_id}`, for example `default/34hdB0nE9cJ2kq4XrVt5wYz7a1M`.",
		)
		return
	}

	cfg, err := r.client.GetAnomalyConfig(ctx, parts[0], parts[1])
	if err != nil {
		summary, detail := anomalyErrorDiag("importing", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if cfg == nil {
		resp.Diagnostics.AddError("Anomaly alert not found",
			fmt.Sprintf("Anomaly alert %q not found in org %q.", parts[1], parts[0]))
		return
	}

	if !requireBandSettings(cfg, &resp.Diagnostics) {
		return
	}

	state := emptyAnomalyModel()
	state.ID = types.StringValue(anomalyResourceID(parts[0], parts[1]))
	state.OrgID = types.StringValue(parts[0])
	state.AnomalyID = types.StringValue(parts[1])
	applyAnomalyToModel(ctx, cfg, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AnomalyAlertResource) refresh(ctx context.Context, org, anomalyID string, model *AnomalyAlertResourceModel, diags *diag.Diagnostics) {
	cfg, err := r.client.GetAnomalyConfig(ctx, org, anomalyID)
	if err != nil {
		summary, detail := anomalyErrorDiag("reading", err)
		diags.AddError(summary, detail)
		return
	}
	if cfg == nil {
		diags.AddError("Anomaly alert not found after write",
			fmt.Sprintf("Anomaly alert %q was not found in org %q after being written.", anomalyID, org))
		return
	}
	if !requireBandSettings(cfg, diags) {
		return
	}
	applyAnomalyToModel(ctx, cfg, model, diags)
}

// requireBandSettings reports a server too old to store the band settings.
// Such a server accepts the request and drops the fields, which would
// otherwise surface as an opaque inconsistent-result error.
func requireBandSettings(cfg *AnomalyConfigAPI, diags *diag.Diagnostics) bool {
	if cfg.HasBandSettings {
		return true
	}
	addBandSettingsError(diags)
	return false
}

func addBandSettingsError(diags *diag.Diagnostics) {
	diags.AddError(
		"OpenObserve is too old for openobserve_anomaly_alert",
		"The server's anomaly alerts have no band settings (band_width, alert_direction, alert_window_*). "+
			"This resource needs OpenObserve Enterprise v"+anomalyMinVersion+" or later. Upgrade the server, "+
			"or remove the resource from the configuration.",
	)
}

// emptyAnomalyModel is a model whose collections are typed nulls, for import.
func emptyAnomalyModel() AnomalyAlertResourceModel {
	return AnomalyAlertResourceModel{
		Filters:           types.ListNull(types.ObjectType{AttrTypes: anomalyFilterAttrTypes}),
		AlertDestinations: types.SetNull(types.StringType),
		Tags:              types.SetNull(types.StringType),
	}
}

// ---------------------------------------------------------------------------
// Model <-> API conversion
// ---------------------------------------------------------------------------

// anomalyWriteFromModel builds the create or update body. Every managed field
// is sent, nullable ones as explicit nulls, so that an update also clears what
// the configuration no longer sets.
func anomalyWriteFromModel(ctx context.Context, m *AnomalyAlertResourceModel, create bool, diags *diag.Diagnostics) AnomalyConfigWriteAPI {
	out := AnomalyConfigWriteAPI{
		Name:                   m.Name.ValueString(),
		Description:            m.Description.ValueString(),
		DetectionFunction:      m.DetectionFunction.ValueString(),
		DetectionFunctionField: optString(m.DetectionFunctionField),
		HistogramInterval:      m.HistogramInterval.ValueString(),
		ScheduleInterval:       m.ScheduleInterval.ValueString(),
		DetectionWindowSeconds: m.DetectionWindowSeconds.ValueInt64(),
		TrainingWindowDays:     m.TrainingWindowDays.ValueInt64(),
		RetrainIntervalDays:    m.RetrainIntervalDays.ValueInt64(),
		AlertBudgetPerDay:      optFloat64(m.AlertBudgetPerDay),
		BandWidth:              optFloat64(m.BandWidth),
		AlertDirection:         optString(m.AlertDirection),
		AlertWindowBuckets:     optInt64(m.AlertWindowBuckets),
		AlertWindowFirePct:     optFloat64(m.AlertWindowFirePct),
		AlertWindowRecoverPct:  optFloat64(m.AlertWindowRecoverPct),
		AlertEnabled:           m.AlertEnabled.ValueBool(),
		AlertDestinations:      nonNilStrings(stringsFromSet(ctx, m.AlertDestinations, diags)),
		Enabled:                m.Enabled.ValueBool(),
		FolderID:               m.FolderID.ValueString(),
		Owner:                  optString(m.Owner),
		Priority:               optInt64(m.Priority),
		Tags:                   nonNilStrings(stringsFromSet(ctx, m.Tags, diags)),
	}
	if create {
		out.StreamName = m.StreamName.ValueString()
		out.StreamType = m.StreamType.ValueString()
	}

	if knownAndSet(m.CustomSQL) {
		out.QueryMode = "custom_sql"
		out.CustomSQL = optString(m.CustomSQL)
	} else {
		out.QueryMode = "filters"
		filters := make([]AnomalyFilterAPI, 0)
		if knownAndSet(m.Filters) {
			var rows []AnomalyFilterModel
			diags.Append(m.Filters.ElementsAs(ctx, &rows, false)...)
			for _, row := range rows {
				filters = append(filters, AnomalyFilterAPI{
					Field:    row.Field.ValueString(),
					Operator: row.Operator.ValueString(),
					Value:    row.Value.ValueString(),
				})
			}
		}
		out.Filters = &filters
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// splitDetectionFunction splits the stored `fn(field)` form. `count(*)` reads
// back as `count` with no field, matching how it is written.
func splitDetectionFunction(combined string) (string, *string) {
	combined = strings.TrimSpace(combined)
	name, rest, ok := strings.Cut(combined, "(")
	if !ok {
		return combined, nil
	}
	field := strings.TrimSpace(strings.TrimSuffix(rest, ")"))
	name = strings.TrimSpace(name)
	if field == "" || field == "*" {
		return name, nil
	}
	return name, &field
}

// applyAnomalyToModel copies a server config into the model.
func applyAnomalyToModel(ctx context.Context, cfg *AnomalyConfigAPI, m *AnomalyAlertResourceModel, diags *diag.Diagnostics) {
	m.Name = types.StringValue(cfg.Name)
	m.Description = types.StringValue("")
	if cfg.Description != nil {
		m.Description = types.StringValue(*cfg.Description)
	}
	m.StreamName = types.StringValue(cfg.StreamName)
	m.StreamType = types.StringValue(cfg.StreamType)
	if cfg.FolderID != "" {
		m.FolderID = types.StringValue(cfg.FolderID)
	} else {
		m.FolderID = types.StringValue("default")
	}

	fn, field := splitDetectionFunction(cfg.DetectionFunction)
	m.DetectionFunction = types.StringValue(fn)
	m.DetectionFunctionField = stringFromPtr(field)

	if cfg.QueryMode == "custom_sql" {
		m.CustomSQL = stringFromPtr(cfg.CustomSQL)
		m.Filters = types.ListNull(types.ObjectType{AttrTypes: anomalyFilterAttrTypes})
	} else {
		m.CustomSQL = types.StringNull()
		m.Filters = anomalyFiltersToList(cfg.Filters, m.Filters, diags)
	}

	m.HistogramInterval = types.StringValue(cfg.HistogramInterval)
	m.ScheduleInterval = types.StringValue(cfg.ScheduleInterval)
	m.DetectionWindowSeconds = types.Int64Value(cfg.DetectionWindowSeconds)
	m.TrainingWindowDays = types.Int64Value(cfg.TrainingWindowDays)
	m.RetrainIntervalDays = types.Int64Value(cfg.RetrainIntervalDays)

	m.AlertBudgetPerDay = float64FromPtr(cfg.AlertBudgetPerDay)
	m.BandWidth = float64FromPtr(cfg.BandWidth)
	// The server stores NULL for "the default"; state holds the default itself.
	m.AlertDirection = types.StringValue("both")
	if cfg.AlertDirection != nil && *cfg.AlertDirection != "" {
		m.AlertDirection = types.StringValue(*cfg.AlertDirection)
	}
	m.AlertWindowBuckets = types.Int64Value(1)
	if cfg.AlertWindowBuckets != nil {
		m.AlertWindowBuckets = types.Int64Value(*cfg.AlertWindowBuckets)
	}
	m.AlertWindowFirePct = types.Float64Value(100)
	if cfg.AlertWindowFirePct != nil {
		m.AlertWindowFirePct = types.Float64Value(*cfg.AlertWindowFirePct)
	}
	m.AlertWindowRecoverPct = float64FromPtr(cfg.AlertWindowRecoverPct)

	m.AlertEnabled = types.BoolValue(cfg.AlertEnabled)
	m.Enabled = types.BoolValue(cfg.Enabled)
	if len(cfg.AlertDestinations) == 0 && (m.AlertDestinations.IsNull() || m.AlertDestinations.IsUnknown()) {
		m.AlertDestinations = types.SetNull(types.StringType)
	} else {
		m.AlertDestinations = setFromStrings(ctx, nonNilStrings(cfg.AlertDestinations), diags)
	}
	if len(cfg.Tags) == 0 && (m.Tags.IsNull() || m.Tags.IsUnknown()) {
		m.Tags = types.SetNull(types.StringType)
	} else {
		m.Tags = setFromStrings(ctx, nonNilStrings(cfg.Tags), diags)
	}
	m.Owner = stringFromPtr(cfg.Owner)
	m.Priority = int64FromPtr(cfg.Priority)

	m.BandGrouping = stringFromPtr(cfg.BandGrouping)
	m.BandK = float64FromPtr(cfg.BandK)
	m.TrainingDataStartUs = int64FromPtr(cfg.TrainingDataStartUs)
	m.TrainingDataEndUs = int64FromPtr(cfg.TrainingDataEndUs)
	m.IsTrained = types.BoolValue(cfg.IsTrained)
	m.Status = types.StringValue(cfg.Status)
}

// anomalyFiltersToList decodes the stored filters. An empty list reads back as
// null unless the configuration wrote an empty list, so leaving `filters` out
// stays clean. A value stored as something other than a string (written by
// another client) is kept as its JSON text.
func anomalyFiltersToList(raw json.RawMessage, prior types.List, diags *diag.Diagnostics) types.List {
	elemType := types.ObjectType{AttrTypes: anomalyFilterAttrTypes}
	var rows []map[string]any
	// Rows written before the server normalized filters can hold `{}`, which
	// it reads as "no filters".
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) > 0 && trimmed != "null" && !isEmptyJSONObject([]byte(trimmed)) {
		if err := json.Unmarshal(raw, &rows); err != nil {
			diags.AddError("Error decoding anomaly alert filters", err.Error())
			return types.ListNull(elemType)
		}
	}
	if len(rows) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
			return prior
		}
		return types.ListNull(elemType)
	}

	text := func(v any) string {
		switch t := v.(type) {
		case nil:
			return ""
		case string:
			return t
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return fmt.Sprint(t)
			}
			return string(b)
		}
	}
	values := make([]attr.Value, 0, len(rows))
	for _, row := range rows {
		values = append(values, objectValue(anomalyFilterAttrTypes, map[string]attr.Value{
			"field":    types.StringValue(text(row["field"])),
			"operator": types.StringValue(text(row["operator"])),
			"value":    types.StringValue(text(row["value"])),
		}, diags))
	}
	list, d := types.ListValue(elemType, values)
	diags.Append(d...)
	return list
}

func anomalyResourceID(orgID, anomalyID string) string {
	return fmt.Sprintf("%s/%s", orgID, anomalyID)
}
