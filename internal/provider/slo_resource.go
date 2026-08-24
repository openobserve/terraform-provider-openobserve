package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &SloResource{}
	_ resource.ResourceWithConfigure      = &SloResource{}
	_ resource.ResourceWithImportState    = &SloResource{}
	_ resource.ResourceWithValidateConfig = &SloResource{}
)

// sloComparators are the orderable operators a time-slice SLO may use. A slice
// with no value is a gap rather than a failure, so equality has no meaning here.
var sloComparators = []string{">", ">=", "<", "<="}

// sloWindows are the rolling windows OpenObserve supports: 7, 30, and 90 days.
var sloWindows = []int64{604800, 2592000, 7776000}

// NewSloResource returns a factory for the openobserve_slo resource.
func NewSloResource() resource.Resource {
	return &SloResource{}
}

// SloResource manages a service level objective.
type SloResource struct {
	client *Client
}

// SloResourceModel holds the Terraform state for an SLO.
type SloResourceModel struct {
	ID           types.String          `tfsdk:"id"`
	SloID        types.String          `tfsdk:"slo_id"`
	OrgID        types.String          `tfsdk:"org_id"`
	FolderID     types.String          `tfsdk:"folder_id"`
	Name         types.String          `tfsdk:"name"`
	Description  types.String          `tfsdk:"description"`
	Target       types.Float64         `tfsdk:"target"`
	WindowSecs   types.Int64           `tfsdk:"window_secs"`
	SliceSecs    types.Int64           `tfsdk:"slice_interval_secs"`
	GroupBy      types.List            `tfsdk:"group_by"`
	Tags         types.Set             `tfsdk:"tags"`
	Enabled      types.Bool            `tfsdk:"enabled"`
	Owner        types.String          `tfsdk:"owner"`
	CountSLI     *SloCountSliModel     `tfsdk:"count_sli"`
	TimeSliceSLI *SloTimeSliceSliModel `tfsdk:"time_slice_sli"`
	AlertSLI     *SloAlertSliModel     `tfsdk:"alert_sli"`
	SliType      types.String          `tfsdk:"sli_type"`
	Generation   types.Int64           `tfsdk:"definition_generation"`
}

// SloCountSliModel is the count_sli block: good events over total events.
type SloCountSliModel struct {
	SingleQuery *SloSingleQueryModel `tfsdk:"single_query"`
	DualQuery   *SloDualQueryModel   `tfsdk:"dual_query"`
	PromQL      *SloPromQLModel      `tfsdk:"promql"`
}

// SloSingleQueryModel counts good and total in one scan.
type SloSingleQueryModel struct {
	Stream     types.String `tfsdk:"stream"`
	StreamType types.String `tfsdk:"stream_type"`
	Scope      types.String `tfsdk:"scope"`
	GoodExpr   types.String `tfsdk:"good_expr"`
}

// SloCountQueryModel is one half of a dual-query count source.
type SloCountQueryModel struct {
	Stream     types.String `tfsdk:"stream"`
	StreamType types.String `tfsdk:"stream_type"`
	SQL        types.String `tfsdk:"sql"`
}

// SloDualQueryModel counts good and total with separate queries.
type SloDualQueryModel struct {
	Good  *SloCountQueryModel `tfsdk:"good"`
	Total *SloCountQueryModel `tfsdk:"total"`
}

// SloPromQLModel counts with a pair of PromQL expressions.
type SloPromQLModel struct {
	Good  types.String `tfsdk:"good"`
	Total types.String `tfsdk:"total"`
}

// SloTimeSliceSliModel is the time_slice_sli block: each slice is good or bad.
type SloTimeSliceSliModel struct {
	Stream        types.String  `tfsdk:"stream"`
	StreamType    types.String  `tfsdk:"stream_type"`
	QueryLanguage types.String  `tfsdk:"query_language"`
	Query         types.String  `tfsdk:"query"`
	Scope         types.String  `tfsdk:"scope"`
	Comparator    types.String  `tfsdk:"comparator"`
	Threshold     types.Float64 `tfsdk:"threshold"`
	AbsentIsBad   types.Bool    `tfsdk:"absent_is_bad"`
}

// SloAlertSliModel is the alert_sli block: an existing alert's firing state.
type SloAlertSliModel struct {
	AlertID types.String `tfsdk:"alert_id"`
}

func (r *SloResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_slo"
}

func (r *SloResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// Attributes inside an optional nested block must be Optional rather than
	// Required: the framework validates a block's Required attributes even when
	// the block itself is absent, which would make every other indicator
	// impossible to write. Presence is enforced in ValidateConfig instead.
	countQueryAttributes := map[string]schema.Attribute{
		"stream": schema.StringAttribute{
			Optional:    true,
			Description: "Stream the query reads.",
		},
		"stream_type": schema.StringAttribute{
			Optional:    true,
			Description: "Stream type: `logs`, `metrics`, or `traces`.",
			Validators:  []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
		},
		"sql": schema.StringAttribute{
			Optional: true,
			Description: "SELECT-only SQL projecting `slice_start`, every `group_by` column, and exactly one " +
				"numeric `zo_slo_value` column.",
		},
	}

	resp.Schema = schema.Schema{
		Description: "Manages a service level objective.\n\n" +
			"An SLO measures a service level indicator over a rolling window and compares it against a `target`. The " +
			"window is divided into slices of `slice_interval_secs`, and each slice contributes a good/total pair.\n\n" +
			"Exactly one indicator block must be given: `count_sli` (good events over total events), " +
			"`time_slice_sli` (each slice is good or bad by threshold), or `alert_sli` (derived from an alert's " +
			"firing state).\n\n" +
			"SLOs live in **alert folders**, since there is no separate SLO folder type, so `folder_id` refers to an " +
			"`openobserve_folder` with `folder_type = \"alerts\"`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{slo_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slo_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned SLO identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the SLO belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("default"),
				Description: "Alert folder holding this SLO. Changing it moves the SLO.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "SLO name, up to 256 characters.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What this objective covers.",
			},
			"target": schema.Float64Attribute{
				Required: true,
				Description: "Target availability as a percentage, between 0 and 100 exclusive, to three decimal " +
					"places. `99.9` means three nines.",
			},
			"window_secs": schema.Int64Attribute{
				Required:    true,
				Description: "Rolling window the objective is measured over, in seconds. 30 days is `2592000`.",
			},
			"slice_interval_secs": schema.Int64Attribute{
				Required:    true,
				Description: "Width of one measurement slice, in seconds. The window must be a whole number of slices.",
			},
			"group_by": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Columns that split the objective into independent per-group measurements. Omit for a single overall figure.",
			},
			"tags": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Selection tags, for example `prod` or `team:checkout`.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the SLO is evaluated. A paused SLO keeps its definition and stops measuring.",
			},
			"owner": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Email of the SLO owner. Defaults to the account the provider authenticates as.",
			},
			"sli_type": schema.StringAttribute{
				Computed:    true,
				Description: "Which indicator block is in use: `count`, `time_slice`, or `alert`.",
			},
			"definition_generation": schema.Int64Attribute{
				Computed: true,
				Description: "Incremented by the server on every edit that changes what a slice means. A bump " +
					"restarts measurement, because slices computed under the old definition are no longer comparable.",
			},
		},
		Blocks: map[string]schema.Block{
			"count_sli": schema.SingleNestedBlock{
				Description: "Count good events against total events. Exactly one of the nested source blocks applies.",
				Blocks: map[string]schema.Block{
					"single_query": schema.SingleNestedBlock{
						Description: "One scan producing both numerator and denominator, so the two are provably " +
							"drawn from the same rows. Prefer this form.",
						Attributes: map[string]schema.Attribute{
							"stream": schema.StringAttribute{
								Optional:    true,
								Description: "Stream to count over.",
							},
							"stream_type": schema.StringAttribute{
								Optional:    true,
								Description: "Stream type: `logs`, `metrics`, or `traces`.",
								Validators:  []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
							},
							"scope": schema.StringAttribute{
								Optional:    true,
								Description: "Denominator filter. Omit to count every row.",
							},
							"good_expr": schema.StringAttribute{
								Optional:    true,
								Description: "Predicate selecting the good rows, for example `status < 500`.",
							},
						},
					},
					"dual_query": schema.SingleNestedBlock{
						Description: "Separate good and total queries. Weaker than `single_query`: the two scans " +
							"cannot be proven to have seen the same instant. Intended for imported definitions " +
							"that cannot be folded into one query.",
						Blocks: map[string]schema.Block{
							"good": schema.SingleNestedBlock{
								Description: "Query producing the numerator.",
								Attributes:  countQueryAttributes,
							},
							"total": schema.SingleNestedBlock{
								Description: "Query producing the denominator.",
								Attributes:  countQueryAttributes,
							},
						},
					},
					"promql": schema.SingleNestedBlock{
						Description: "A pair of PromQL expressions, for pre-aggregated counters where `good` only " +
							"exists as arithmetic between series. Use a range selector equal to the slice " +
							"interval, for example `increase(http_requests_total[5m])` with a 5-minute slice.",
						Attributes: map[string]schema.Attribute{
							"good": schema.StringAttribute{
								Optional:    true,
								Description: "Expression producing the numerator.",
							},
							"total": schema.StringAttribute{
								Optional:    true,
								Description: "Expression producing the denominator.",
							},
						},
					},
				},
			},
			"time_slice_sli": schema.SingleNestedBlock{
				Description: "Mark each slice good or bad by comparing an aggregate against a threshold.",
				Attributes: map[string]schema.Attribute{
					"stream": schema.StringAttribute{
						Optional:    true,
						Description: "Stream the query reads.",
					},
					"stream_type": schema.StringAttribute{
						Optional:    true,
						Description: "Stream type: `logs`, `metrics`, or `traces`.",
						Validators:  []validator.String{stringvalidator.OneOf("logs", "metrics", "traces")},
					},
					"query_language": schema.StringAttribute{
						Optional:    true,
						Description: "Language of `query`: `sql` or `prom_ql`. Carried explicitly so the evaluator never guesses.",
						Validators:  []validator.String{stringvalidator.OneOf("sql", "prom_ql")},
					},
					"query": schema.StringAttribute{
						Optional:    true,
						Description: "Aggregate evaluated once per slice.",
					},
					"scope": schema.StringAttribute{
						Optional:    true,
						Description: "Additional filter applied to the query.",
					},
					"comparator": schema.StringAttribute{
						Optional: true,
						Description: "How the aggregate is compared against `threshold`: `>`, `>=`, `<`, or `<=`. " +
							"Only orderable comparators are meaningful, because a slice with no value is a gap " +
							"rather than a failure.",
						Validators: []validator.String{stringvalidator.OneOf(sloComparators...)},
					},
					"threshold": schema.Float64Attribute{
						Optional:    true,
						Description: "Value the aggregate is compared against.",
					},
					"absent_is_bad": schema.BoolAttribute{
						Optional: true,
						Description: "Treat a slice the query proved empty as bad rather than as a gap. For a " +
							"pipeline-freshness objective, silence is the failure signal. Ungrouped SLOs only.",
					},
				},
			},
			"alert_sli": schema.SingleNestedBlock{
				Description: "Derive the indicator from an existing alert: slices where the alert was firing count as bad.",
				Attributes: map[string]schema.Attribute{
					"alert_id": schema.StringAttribute{
						Optional:    true,
						Description: "Identifier of the alert to read, as produced by `openobserve_alert.alert_id`.",
					},
				},
			},
		},
	}
}

// ValidateConfig enforces the exactly-one-indicator rule up front, rather than
// letting the server reject the apply.
func (r *SloResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config SloResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The server accepts only three rolling windows, and a target outside
	// (0, 100). Both are cheap to check before the apply.
	if !config.WindowSecs.IsNull() && !config.WindowSecs.IsUnknown() {
		w := config.WindowSecs.ValueInt64()
		supported := false
		for _, candidate := range sloWindows {
			if w == candidate {
				supported = true
				break
			}
		}
		if !supported {
			resp.Diagnostics.AddAttributeError(
				path.Root("window_secs"),
				"Unsupported rolling window",
				fmt.Sprintf("`window_secs` must be one of 604800 (7 days), 2592000 (30 days), or 7776000 "+
					"(90 days). Got %d.", w),
			)
		}
	}
	if !config.Target.IsNull() && !config.Target.IsUnknown() {
		target := config.Target.ValueFloat64()
		if target <= 0 || target >= 100 {
			resp.Diagnostics.AddAttributeError(
				path.Root("target"),
				"Target out of range",
				"`target` must be greater than 0 and strictly below 100. A 100% target leaves a zero error "+
					"budget, so every burn rate would be either 0 or infinite.",
			)
		}
	}

	// Read the indicators from the raw configuration rather than from the
	// decoded pointers. A `dynamic` block that produced no iterations still
	// arrives as a present object full of unknowns, so a nil check reports
	// indicators the author never wrote. See blockPresence in helpers.go.
	countPresence := nestedBlockPresence(ctx, req.Config, path.Root("count_sli"))
	timeSlicePresence := nestedBlockPresence(ctx, req.Config, path.Root("time_slice_sli"))
	alertPresence := nestedBlockPresence(ctx, req.Config, path.Root("alert_sli"))

	indicators, undecided := 0, 0
	for _, p := range []blockPresence{countPresence, timeSlicePresence, alertPresence} {
		switch p {
		case blockConfigured:
			indicators++
		case blockIndeterminate:
			undecided++
		}
	}

	switch {
	case indicators > 1:
		resp.Diagnostics.AddError(
			"Conflicting service level indicators",
			"Exactly one of `count_sli`, `time_slice_sli`, or `alert_sli` may be set.",
		)
		return
	case indicators == 0 && undecided == 0:
		resp.Diagnostics.AddError(
			"Missing service level indicator",
			"Exactly one of `count_sli`, `time_slice_sli`, or `alert_sli` must be set.",
		)
		return
	case undecided > 0:
		// Something here is still unknown, so which indicators were written
		// cannot be decided. The server enforces the same rule on apply.
		return
	}

	if countPresence == blockConfigured && config.CountSLI != nil {
		sourcePaths := map[string]blockPresence{
			"single_query": nestedBlockPresence(ctx, req.Config, path.Root("count_sli").AtName("single_query")),
			"dual_query":   nestedBlockPresence(ctx, req.Config, path.Root("count_sli").AtName("dual_query")),
			"promql":       nestedBlockPresence(ctx, req.Config, path.Root("count_sli").AtName("promql")),
		}
		sources, undecidedSources := 0, 0
		for _, p := range sourcePaths {
			switch p {
			case blockConfigured:
				sources++
			case blockIndeterminate:
				undecidedSources++
			}
		}
		if undecidedSources > 0 {
			return
		}
		if sources != 1 {
			resp.Diagnostics.AddError(
				"Invalid count_sli source",
				"Exactly one of `single_query`, `dual_query`, or `promql` must be set inside `count_sli`.",
			)
		}

		if sq := config.CountSLI.SingleQuery; sq != nil && sourcePaths["single_query"] == blockConfigured {
			requireSloAttr(resp, sq.Stream, "count_sli", "single_query", "stream")
			requireSloAttr(resp, sq.StreamType, "count_sli", "single_query", "stream_type")
			requireSloAttr(resp, sq.GoodExpr, "count_sli", "single_query", "good_expr")
		}
		if dq := config.CountSLI.DualQuery; dq != nil && sourcePaths["dual_query"] == blockConfigured {
			if dq.Good == nil || dq.Total == nil {
				resp.Diagnostics.AddError(
					"Incomplete dual_query source",
					"Both `good` and `total` must be given inside `dual_query`.",
				)
			}
		}
		if pq := config.CountSLI.PromQL; pq != nil && sourcePaths["promql"] == blockConfigured {
			requireSloAttr(resp, pq.Good, "count_sli", "promql", "good")
			requireSloAttr(resp, pq.Total, "count_sli", "promql", "total")
		}
	}

	if ts := config.TimeSliceSLI; ts != nil && timeSlicePresence == blockConfigured {
		requireSloAttr(resp, ts.Stream, "time_slice_sli", "", "stream")
		requireSloAttr(resp, ts.StreamType, "time_slice_sli", "", "stream_type")
		requireSloAttr(resp, ts.QueryLanguage, "time_slice_sli", "", "query_language")
		requireSloAttr(resp, ts.Query, "time_slice_sli", "", "query")
		requireSloAttr(resp, ts.Comparator, "time_slice_sli", "", "comparator")
		if ts.Threshold.IsNull() {
			resp.Diagnostics.AddError(
				"Missing time_slice_sli.threshold",
				"`threshold` is required inside `time_slice_sli`.",
			)
		}
	}

	if a := config.AlertSLI; a != nil && alertPresence == blockConfigured {
		requireSloAttr(resp, a.AlertID, "alert_sli", "", "alert_id")
	}

	// A grouped freshness objective could never fire for the failure it
	// watches, because gap fill cannot see a group absent from the whole pass.
	if timeSlicePresence == blockConfigured && config.TimeSliceSLI != nil &&
		knownAndSet(config.TimeSliceSLI.AbsentIsBad) && config.TimeSliceSLI.AbsentIsBad.ValueBool() &&
		knownAndSet(config.GroupBy) && len(config.GroupBy.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(
			path.Root("time_slice_sli").AtName("absent_is_bad"),
			"absent_is_bad cannot be combined with group_by",
			"Gap fill cannot observe a group that is missing from an entire pass, so a grouped objective would "+
				"freeze rather than fire. Remove `group_by`, or leave `absent_is_bad` unset.",
		)
	}
}

// requireSloAttr reports a missing attribute inside an indicator block, which
// the schema cannot mark Required without breaking the other indicators.
func requireSloAttr(resp *resource.ValidateConfigResponse, v types.String, block, sub, name string) {
	if !v.IsNull() {
		return
	}
	where := block
	p := path.Root(block)
	if sub != "" {
		where = block + "." + sub
		p = p.AtName(sub)
	}
	resp.Diagnostics.AddAttributeError(
		p.AtName(name),
		"Missing "+where+"."+name,
		"`"+name+"` is required inside `"+where+"`.",
	)
}

func (r *SloResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *SloResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SloResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.sloFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	sloID, err := r.client.CreateSlo(ctx, org, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating SLO", err.Error())
		return
	}
	if sloID == "" {
		found, lookupErr := r.client.FindSloByName(ctx, org, plan.FolderID.ValueString(), plan.Name.ValueString())
		if lookupErr != nil || found == nil {
			resp.Diagnostics.AddError(
				"Error resolving SLO after create",
				"The SLO was created but the server did not return its ID, and looking it up by name failed.",
			)
			return
		}
		sloID = found.ID
	}

	plan.OrgID = types.StringValue(org)
	plan.SloID = types.StringValue(sloID)
	plan.ID = types.StringValue(sloResourceID(org, sloID))

	// Enablement is a separate endpoint, so a create asking for a paused SLO
	// needs a follow-up call.
	if !plan.Enabled.ValueBool() {
		if err := r.client.SetSloEnabled(ctx, org, sloID, false); err != nil {
			resp.Diagnostics.AddError("Error pausing SLO after create", err.Error())
			return
		}
	}

	r.refresh(ctx, org, sloID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SloResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SloResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	sloID := state.SloID.ValueString()

	slo, err := r.client.GetSlo(ctx, org, sloID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading SLO", err.Error())
		return
	}
	if slo == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(sloResourceID(org, sloID))
	r.applySloToModel(ctx, slo, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SloResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SloResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.sloFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	sloID := state.SloID.ValueString()
	body.ID = sloID

	if err := r.client.UpdateSlo(ctx, org, sloID, body); err != nil {
		resp.Diagnostics.AddError("Error updating SLO", err.Error())
		return
	}

	if !plan.Enabled.Equal(state.Enabled) {
		if err := r.client.SetSloEnabled(ctx, org, sloID, plan.Enabled.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error changing SLO enablement", err.Error())
			return
		}
	}

	folderID := plan.FolderID.ValueString()
	if folderID != "" && folderID != state.FolderID.ValueString() {
		if err := r.client.MoveSlos(ctx, org, folderID, []string{sloID}); err != nil {
			resp.Diagnostics.AddError("Error moving SLO between folders", err.Error())
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.SloID = types.StringValue(sloID)
	plan.ID = types.StringValue(sloResourceID(org, sloID))
	r.refresh(ctx, org, sloID, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SloResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SloResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSlo(ctx, org, state.SloID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting SLO", err.Error())
	}
}

// ImportState supports: terraform import openobserve_slo.example default/2abcXYZ
func (r *SloResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{slo_id}`, for example `default/2abcXYZ`.",
		)
		return
	}

	slo, err := r.client.GetSlo(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading SLO during import", err.Error())
		return
	}
	if slo == nil {
		resp.Diagnostics.AddError("SLO not found", fmt.Sprintf("SLO %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := SloResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		SloID: types.StringValue(parts[1]),
	}
	r.applySloToModel(ctx, slo, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ---------------------------------------------------------------------------
// Model <-> API conversion
// ---------------------------------------------------------------------------

func (r *SloResource) sloFromModel(ctx context.Context, model *SloResourceModel, diags *diag.Diagnostics) SloAPI {
	out := SloAPI{
		FolderID:    model.FolderID.ValueString(),
		Name:        model.Name.ValueString(),
		Description: model.Description.ValueString(),
		GroupBy:     stringsFromList(ctx, model.GroupBy, diags),
		WindowSecs:  model.WindowSecs.ValueInt64(),
		SliceSecs:   model.SliceSecs.ValueInt64(),
		Target:      model.Target.ValueFloat64(),
		Tags:        stringsFromSet(ctx, model.Tags, diags),
		Enabled:     model.Enabled.ValueBool(),
		Owner:       optString(model.Owner),
	}

	switch {
	case model.CountSLI != nil:
		out.SliType = "count"
		out.Config = countConfigJSON(model.CountSLI, diags)
	case model.TimeSliceSLI != nil:
		out.SliType = "time_slice"
		ts := model.TimeSliceSLI
		out.Config = marshalSloConfig(SloTimeSliceConfigAPI{
			Stream:        ts.Stream.ValueString(),
			StreamType:    ts.StreamType.ValueString(),
			QueryLanguage: ts.QueryLanguage.ValueString(),
			Query:         ts.Query.ValueString(),
			Scope:         optString(ts.Scope),
			Comparator:    ts.Comparator.ValueString(),
			Threshold:     ts.Threshold.ValueFloat64(),
			AbsentIsBad:   ts.AbsentIsBad.ValueBool(),
		}, diags)
	case model.AlertSLI != nil:
		out.SliType = "alert"
		out.Config = marshalSloConfig(SloAlertConfigAPI{
			AlertID: model.AlertSLI.AlertID.ValueString(),
		}, diags)
	}

	return out
}

// countConfigJSON builds a count indicator's config.
//
// Two levels of tagging meet here: the count variant is a struct variant with a
// `source` field, and that source is itself adjacently tagged by `mode`. The
// result is `{"source": {"mode": …, "query": …}}`.
func countConfigJSON(m *SloCountSliModel, diags *diag.Diagnostics) json.RawMessage {
	var mode string
	var query any

	switch {
	case m.SingleQuery != nil:
		mode = "single_query"
		query = SloCountSingleQueryAPI{
			Stream:     m.SingleQuery.Stream.ValueString(),
			StreamType: m.SingleQuery.StreamType.ValueString(),
			Scope:      optString(m.SingleQuery.Scope),
			GoodExpr:   m.SingleQuery.GoodExpr.ValueString(),
		}
	case m.DualQuery != nil:
		mode = "dual_query"
		dual := SloCountDualQueryAPI{}
		if m.DualQuery.Good != nil {
			dual.Good = SloCountQueryAPI{
				Stream:     m.DualQuery.Good.Stream.ValueString(),
				StreamType: m.DualQuery.Good.StreamType.ValueString(),
				SQL:        m.DualQuery.Good.SQL.ValueString(),
			}
		}
		if m.DualQuery.Total != nil {
			dual.Total = SloCountQueryAPI{
				Stream:     m.DualQuery.Total.Stream.ValueString(),
				StreamType: m.DualQuery.Total.StreamType.ValueString(),
				SQL:        m.DualQuery.Total.SQL.ValueString(),
			}
		}
		query = dual
	case m.PromQL != nil:
		mode = "prom_ql"
		query = SloCountPromQLAPI{
			Good:  m.PromQL.Good.ValueString(),
			Total: m.PromQL.Total.ValueString(),
		}
	default:
		return nil
	}

	encoded, err := json.Marshal(map[string]any{
		"source": map[string]any{"mode": mode, "query": query},
	})
	if err != nil {
		diags.AddError("Error encoding SLO count source", err.Error())
		return nil
	}
	return encoded
}

func marshalSloConfig(v any, diags *diag.Diagnostics) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		diags.AddError("Error encoding SLO configuration", err.Error())
		return nil
	}
	return encoded
}

func (r *SloResource) refresh(ctx context.Context, org, sloID string, model *SloResourceModel, diags *diag.Diagnostics) {
	slo, err := r.client.GetSlo(ctx, org, sloID)
	if err != nil {
		diags.AddError("Error reading SLO after write", err.Error())
		return
	}
	if slo == nil {
		diags.AddError("SLO not found after write", fmt.Sprintf("SLO %q was not found in org %q after being written.", sloID, org))
		return
	}
	r.applySloToModel(ctx, slo, model, diags)
}

func (r *SloResource) applySloToModel(ctx context.Context, slo *SloAPI, model *SloResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(slo.Name)
	model.Description = types.StringValue(slo.Description)
	model.Target = types.Float64Value(slo.Target)
	model.WindowSecs = types.Int64Value(slo.WindowSecs)
	model.SliceSecs = types.Int64Value(slo.SliceSecs)
	model.Enabled = types.BoolValue(slo.Enabled)
	model.Owner = stringFromPtr(slo.Owner)
	model.SliType = types.StringValue(slo.SliType)
	model.Generation = types.Int64Value(int64(slo.DefinitionGeneration))

	if slo.FolderID != "" {
		model.FolderID = types.StringValue(slo.FolderID)
	}
	if len(slo.GroupBy) == 0 {
		model.GroupBy = types.ListNull(types.StringType)
	} else {
		model.GroupBy = listFromStrings(ctx, slo.GroupBy, diags)
	}
	if len(slo.Tags) == 0 {
		model.Tags = types.SetNull(types.StringType)
	} else {
		model.Tags = setFromStrings(ctx, slo.Tags, diags)
	}

	r.applySliConfigToModel(slo, model, diags)
}

// applySliConfigToModel decodes the indicator config back into the matching
// block. The configured block is preserved rather than rebuilt where the server
// echoes it unchanged, so optional fields the user omitted stay omitted.
func (r *SloResource) applySliConfigToModel(slo *SloAPI, model *SloResourceModel, diags *diag.Diagnostics) {
	switch slo.SliType {
	case "count":
		var wrapper struct {
			Source struct {
				Mode  string          `json:"mode"`
				Query json.RawMessage `json:"query"`
			} `json:"source"`
		}
		if err := json.Unmarshal(slo.Config, &wrapper); err != nil {
			diags.AddError("Error decoding SLO count source", err.Error())
			return
		}
		envelope := wrapper.Source
		count := &SloCountSliModel{}
		switch envelope.Mode {
		case "single_query":
			var q SloCountSingleQueryAPI
			if err := json.Unmarshal(envelope.Query, &q); err != nil {
				diags.AddError("Error decoding SLO single-query source", err.Error())
				return
			}
			count.SingleQuery = &SloSingleQueryModel{
				Stream:     types.StringValue(q.Stream),
				StreamType: types.StringValue(q.StreamType),
				Scope:      stringFromPtr(q.Scope),
				GoodExpr:   types.StringValue(q.GoodExpr),
			}
		case "dual_query":
			var q SloCountDualQueryAPI
			if err := json.Unmarshal(envelope.Query, &q); err != nil {
				diags.AddError("Error decoding SLO dual-query source", err.Error())
				return
			}
			count.DualQuery = &SloDualQueryModel{
				Good: &SloCountQueryModel{
					Stream:     types.StringValue(q.Good.Stream),
					StreamType: types.StringValue(q.Good.StreamType),
					SQL:        types.StringValue(q.Good.SQL),
				},
				Total: &SloCountQueryModel{
					Stream:     types.StringValue(q.Total.Stream),
					StreamType: types.StringValue(q.Total.StreamType),
					SQL:        types.StringValue(q.Total.SQL),
				},
			}
		case "prom_ql":
			var q SloCountPromQLAPI
			if err := json.Unmarshal(envelope.Query, &q); err != nil {
				diags.AddError("Error decoding SLO PromQL source", err.Error())
				return
			}
			count.PromQL = &SloPromQLModel{
				Good:  types.StringValue(q.Good),
				Total: types.StringValue(q.Total),
			}
		}
		model.CountSLI = count
		model.TimeSliceSLI = nil
		model.AlertSLI = nil

	case "time_slice":
		var priorAbsentIsBad types.Bool
		if model.TimeSliceSLI != nil {
			priorAbsentIsBad = model.TimeSliceSLI.AbsentIsBad
		}
		var c SloTimeSliceConfigAPI
		if err := json.Unmarshal(slo.Config, &c); err != nil {
			diags.AddError("Error decoding SLO time-slice configuration", err.Error())
			return
		}
		model.TimeSliceSLI = &SloTimeSliceSliModel{
			Stream:        types.StringValue(c.Stream),
			StreamType:    types.StringValue(c.StreamType),
			QueryLanguage: types.StringValue(c.QueryLanguage),
			Query:         types.StringValue(c.Query),
			Scope:         stringFromPtr(c.Scope),
			Comparator:    types.StringValue(c.Comparator),
			Threshold:     types.Float64Value(c.Threshold),
			AbsentIsBad:   boolPreserveNull(priorAbsentIsBad, c.AbsentIsBad),
		}
		model.CountSLI = nil
		model.AlertSLI = nil

	case "alert":
		var c SloAlertConfigAPI
		if err := json.Unmarshal(slo.Config, &c); err != nil {
			diags.AddError("Error decoding SLO alert configuration", err.Error())
			return
		}
		model.AlertSLI = &SloAlertSliModel{AlertID: types.StringValue(c.AlertID)}
		model.CountSLI = nil
		model.TimeSliceSLI = nil
	}
}

func sloResourceID(orgID, sloID string) string {
	return fmt.Sprintf("%s/%s", orgID, sloID)
}
