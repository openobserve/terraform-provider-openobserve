package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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
	_ resource.Resource                   = &DowntimeResource{}
	_ resource.ResourceWithConfigure      = &DowntimeResource{}
	_ resource.ResourceWithImportState    = &DowntimeResource{}
	_ resource.ResourceWithValidateConfig = &DowntimeResource{}
	_ resource.ResourceWithModifyPlan     = &DowntimeResource{}
)

// The server's limits on a downtime.
const (
	downtimeMinDurationSecs  = 60
	downtimeMaxDurationSecs  = 7 * 24 * 3600
	downtimeMaxNameLen       = 256
	downtimeMaxReasonLen     = 1000
	downtimeMaxFolders       = 50
	downtimeMaxIDs           = 200
	downtimeMaxTags          = 16
	downtimeMaxDestinations  = 10
	downtimeMinEndingSoonSec = 60
	downtimeDefaultLeadSecs  = 600
)

var (
	downtimeModules      = []string{"alerts", "anomaly_detections", "synthetics", "slos"}
	downtimeEvents       = []string{"started", "ending_soon", "ended", "cancelled", "extended"}
	downtimeLocalTimeRE  = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	downtimeTargetAttrTy = map[string]attr.Type{
		"module":        types.StringType,
		"folder_ids":    types.SetType{ElemType: types.StringType},
		"tags":          types.SetType{ElemType: types.StringType},
		"ids":           types.SetType{ElemType: types.StringType},
		"slo_mode":      types.StringType,
		"incident_mode": types.StringType,
	}
)

// NewDowntimeResource returns a factory for the openobserve_downtime resource.
func NewDowntimeResource() resource.Resource {
	return &DowntimeResource{}
}

// DowntimeResource manages a maintenance window.
type DowntimeResource struct {
	client *Client
}

// DowntimeResourceModel holds the Terraform state for a downtime.
type DowntimeResourceModel struct {
	ID            types.String                `tfsdk:"id"`
	DowntimeID    types.String                `tfsdk:"downtime_id"`
	OrgID         types.String                `tfsdk:"org_id"`
	FolderID      types.String                `tfsdk:"folder_id"`
	Name          types.String                `tfsdk:"name"`
	Reason        types.String                `tfsdk:"reason"`
	Condition     types.String                `tfsdk:"condition"`
	ShowBanner    types.Bool                  `tfsdk:"show_banner"`
	Status        types.String                `tfsdk:"status"`
	Targets       types.List                  `tfsdk:"target"`
	Schedule      *DowntimeScheduleModel      `tfsdk:"schedule"`
	Notifications *DowntimeNotificationsModel `tfsdk:"notifications"`
}

// DowntimeTargetModel is one target block.
type DowntimeTargetModel struct {
	Module       types.String `tfsdk:"module"`
	FolderIDs    types.Set    `tfsdk:"folder_ids"`
	Tags         types.Set    `tfsdk:"tags"`
	IDs          types.Set    `tfsdk:"ids"`
	SloMode      types.String `tfsdk:"slo_mode"`
	IncidentMode types.String `tfsdk:"incident_mode"`
}

// DowntimeScheduleModel is the schedule block.
type DowntimeScheduleModel struct {
	Repeat         types.String `tfsdk:"repeat"`
	StartsAt       types.String `tfsdk:"starts_at"`
	EndsAt         types.String `tfsdk:"ends_at"`
	Timezone       types.String `tfsdk:"timezone"`
	StartTimeLocal types.String `tfsdk:"start_time_local"`
	DurationSecs   types.Int64  `tfsdk:"duration_secs"`
	Weekdays       types.Set    `tfsdk:"weekdays"`
}

// DowntimeNotificationsModel is the notifications block.
type DowntimeNotificationsModel struct {
	Destinations       types.Set   `tfsdk:"destinations"`
	Events             types.Set   `tfsdk:"events"`
	EndingSoonLeadSecs types.Int64 `tfsdk:"ending_soon_lead_secs"`
}

func (r *DowntimeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_downtime"
}

func (r *DowntimeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve downtime: a maintenance window during which alerts, anomaly " +
			"detections and synthetics checks are muted, and SLOs are corrected.\n\n" +
			"**Requires OpenObserve Enterprise v1.2.0-rc1 or later.** An open-source or older server is " +
			"refused with a diagnostic saying so.\n\n" +
			"A downtime is one-time (`schedule.repeat = \"none\"`, from `starts_at` to `ends_at`) or recurring " +
			"(`daily` or `weekly`, a window of `duration_secs` starting at `start_time_local` in `timezone`). " +
			"Each `target` block picks a module and narrows it by folder, ID or (for synthetics) tag, and " +
			"`condition` narrows every module but synthetics by the dimensions of what fires.\n\n" +
			"The server keeps a downtime after it ends, for history, and an ended one-time downtime stays in " +
			"state with `status = \"ended\"`. A downtime cancelled in the UI also stays, with `status = " +
			"\"cancelled\"` or `\"ended_early\"`, and is not scheduled again by an update: replace it (for " +
			"example with `terraform apply -replace`) to schedule it anew. The server deletes ended and " +
			"cancelled downtimes after its retention period (`O2_DOWNTIMES_RETENTION_DAYS`, 90 days or more). " +
			"One that was ended or cancelled when it was last read then stays in state as it was, rather " +
			"than being created again, so a cancelled window does not come back.\n\n" +
			"Destroying a downtime that is active or scheduled cancels it first, which the server requires, " +
			"then deletes it. A downtime that already corrected SLO slices cannot be deleted at all: it is " +
			"cancelled, removed from state with a warning, and removed by the server's retention once the " +
			"SLO window has passed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{downtime_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"downtime_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned downtime ID.",
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
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("default"),
				Description: "Downtime folder that holds the downtime, from an `openobserve_folder` with " +
					"`folder_type = \"downtimes\"`. Defaults to `default`. Changing it moves the downtime in place.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Name shown in the downtime list and on the banner, at most 256 characters, with no " +
					"leading or trailing spaces.",
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, downtimeMaxNameLen),
					trimmedStringValidator{},
				},
			},
			"reason": schema.StringAttribute{
				Optional:    true,
				Description: "Why the downtime exists, at most 1000 characters.",
				Validators:  []validator.String{stringvalidator.UTF8LengthAtMost(downtimeMaxReasonLen)},
			},
			"condition": schema.StringAttribute{
				Optional: true,
				Description: "Dimension condition, as a JSON document; build it with `jsonencode()`. It narrows " +
					"every target whose module has dimensions (all but synthetics) to the items it matches. " +
					"A pair is `{type = \"pair\", key = \"service\", operator = \"=\", value = \"checkout\"}`, " +
					"with `operator` `=` or `!=`, and a `value` that may end in `*` to match a prefix. A group " +
					"is `{type = \"group\", op = \"and\", items = [...]}`, with `op` `and` or `or`.\n\n" +
					"Keys must be written exactly, with no surrounding spaces. The server lowercases values, so " +
					"`Checkout` matches `checkout`, and every " +
					"way to satisfy the condition must include an `=` pair: `!=` alone cannot silence " +
					"everything. Keys are the semantic dimensions of the organization (such as `service` or " +
					"`env`); the server refuses a key it does not know.",
			},
			"show_banner": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Show a warning banner to everyone in the organization while a window is active. Defaults to `true`.",
			},
			"status": schema.StringAttribute{
				Computed: true,
				Description: "Where the downtime is now: `scheduled`, `active`, `ended`, `cancelled`, or " +
					"`ended_early` (cancelled during a window). Read when the resource is refreshed.",
			},
		},
		Blocks: map[string]schema.Block{
			"target": schema.ListNestedBlock{
				Description: "A module the downtime covers, at most one block per module. At least one is required.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"module": schema.StringAttribute{
							Required:    true,
							Description: "`alerts`, `anomaly_detections`, `synthetics` or `slos`.",
							Validators:  []validator.String{stringvalidator.OneOf(downtimeModules...)},
						},
						"folder_ids": schema.SetAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "Folders of the module to cover, at most 50. Omit it to cover every folder.",
							Validators:  []validator.Set{setvalidator.SizeBetween(1, downtimeMaxFolders)},
						},
						"ids": schema.SetAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "IDs of the items to cover, at most 200: alert IDs, anomaly detection IDs, " +
								"synthetics check IDs or SLO IDs. Omit it to cover every item the other settings select.",
							Validators: []validator.Set{setvalidator.SizeBetween(1, downtimeMaxIDs)},
						},
						"tags": schema.SetAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "Synthetics only: tags of the checks to cover, at most 16, in lowercase.",
							Validators: []validator.Set{
								setvalidator.SizeBetween(1, downtimeMaxTags),
								setvalidator.ValueStringsAre(lowercaseTrimmedValidator{}),
							},
						},
						"slo_mode": schema.StringAttribute{
							Optional: true,
							Description: "SLOs only: how the window counts toward the SLO. `exclude` leaves its " +
								"minutes out, `count_as_good` counts them as good. The server's default is `exclude`.",
							Validators: []validator.String{stringvalidator.OneOf("exclude", "count_as_good")},
						},
						"incident_mode": schema.StringAttribute{
							Optional: true,
							Computed: true,
							Default:  stringdefault.StaticString("muted"),
							Description: "Alerts only: what a muted firing does to incidents. `muted`, the default, " +
								"records the incident as muted; `none` opens no incident at all.",
							Validators: []validator.String{stringvalidator.OneOf("muted", "none")},
						},
					},
				},
			},
			"schedule": schema.SingleNestedBlock{
				Description: "When the downtime applies. Required.",
				Attributes: map[string]schema.Attribute{
					"repeat": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString("none"),
						Description: "`none` (one-time, the default), `daily` or `weekly`.",
						Validators:  []validator.String{stringvalidator.OneOf("none", "daily", "weekly")},
					},
					"starts_at": schema.StringAttribute{
						Optional: true,
						Description: "Start, as an RFC 3339 timestamp such as `2026-12-01T22:00:00Z`. For a " +
							"recurring downtime it is the first day the schedule applies from. Required.",
						Validators: []validator.String{rfc3339Validator{}},
					},
					"ends_at": schema.StringAttribute{
						Optional: true,
						Description: "End, as an RFC 3339 timestamp. Required for a one-time downtime, whose " +
							"window runs from `starts_at` to `ends_at`, at least a minute and at most 7 days. For a " +
							"recurring downtime it is optional, and only its date in `timezone` counts: the last " +
							"day the schedule applies, inclusive.",
						Validators: []validator.String{rfc3339Validator{}},
					},
					"timezone": schema.StringAttribute{
						Optional:    true,
						Computed:    true,
						Default:     stringdefault.StaticString("UTC"),
						Description: "IANA timezone of `start_time_local` and of a recurring downtime's days, such as `Europe/Berlin`. Defaults to `UTC`.",
					},
					"start_time_local": schema.StringAttribute{
						Optional:    true,
						Description: "Recurring downtimes only: when each window starts, as `HH:MM` in `timezone`. Required for `daily` and `weekly`.",
						Validators: []validator.String{stringvalidator.RegexMatches(downtimeLocalTimeRE,
							"must be a 24-hour time as HH:MM, such as 02:30")},
					},
					"duration_secs": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Description: "Recurring downtimes only: how long each window lasts, from 60 seconds to 7 " +
							"days, and no longer than the time between repeats. Required for `daily` and `weekly`. " +
							"A one-time downtime reports its span here.",
						Validators: []validator.Int64{int64validator.Between(downtimeMinDurationSecs, downtimeMaxDurationSecs)},
					},
					"weekdays": schema.SetAttribute{
						Optional:    true,
						ElementType: types.Int64Type,
						Description: "Weekly downtimes only: the days a window starts, 1 (Monday) to 7 (Sunday).",
						Validators: []validator.Set{
							setvalidator.SizeAtLeast(1),
							setvalidator.ValueInt64sAre(int64validator.Between(1, 7)),
						},
					},
				},
			},
			"notifications": schema.SingleNestedBlock{
				Description: "Announce the downtime's events to alert destinations. Omit it to announce nothing.",
				Attributes: map[string]schema.Attribute{
					"destinations": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "Names of `openobserve_alert_destination` resources to notify, 1 to 10. Required in the block.",
						Validators:  []validator.Set{setvalidator.SizeBetween(1, downtimeMaxDestinations)},
					},
					"events": schema.SetAttribute{
						Optional:    true,
						ElementType: types.StringType,
						Description: "Events to announce: `started`, `ending_soon`, `ended`, `cancelled` and `extended`. At least one is required in the block.",
						Validators: []validator.Set{
							setvalidator.SizeAtLeast(1),
							setvalidator.ValueStringsAre(stringvalidator.OneOf(downtimeEvents...)),
						},
					},
					"ending_soon_lead_secs": schema.Int64Attribute{
						Optional: true,
						Computed: true,
						Default:  int64default.StaticInt64(downtimeDefaultLeadSecs),
						Description: "How long before the end the `ending_soon` event is sent, in seconds. Defaults " +
							"to 600. With `ending_soon`, it must be at least 60 and no longer than the window.",
					},
				},
			},
		},
	}
}

func (r *DowntimeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// ---------------------------------------------------------------------------
// Validation and planning
// ---------------------------------------------------------------------------

func (r *DowntimeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config DowntimeResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateDowntimeConfig(ctx, &config, &resp.Diagnostics)
}

func validateDowntimeConfig(ctx context.Context, config *DowntimeResourceModel, diags *diag.Diagnostics) {
	targets, known := downtimeTargets(ctx, config.Targets, diags)
	if known {
		validateDowntimeTargets(targets, diags)
	}

	if knownAndSet(config.Condition) {
		if _, err := parseDowntimeCondition(config.Condition.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("condition"), "Invalid condition", err.Error())
		} else if known && len(targets) > 0 && !anyModuleWithDimensions(targets) {
			diags.AddAttributeError(path.Root("condition"), "Condition has nothing to narrow",
				"Synthetics checks have no dimensions, so a condition cannot narrow them. Narrow them by tag, "+
					"folder or ID, or add a target whose module has dimensions.")
		}
	}

	s := config.Schedule
	if s == nil {
		diags.AddAttributeError(path.Root("schedule"), "Missing schedule", "A `schedule` block is required.")
		return
	}
	window, windowKnown := validateDowntimeSchedule(s, diags)

	if n := config.Notifications; n != nil {
		root := path.Root("notifications")
		if n.Destinations.IsNull() {
			diags.AddAttributeError(root.AtName("destinations"), "Missing destinations",
				"`destinations` is required in a `notifications` block. Remove the block to announce nothing.")
		}
		if n.Events.IsNull() {
			diags.AddAttributeError(root.AtName("events"), "Missing events",
				"`events` is required in a `notifications` block: name at least one event to announce.")
		}
		// The default lead is not applied yet when the configuration is checked.
		lead, leadKnown := int64(downtimeDefaultLeadSecs), !n.EndingSoonLeadSecs.IsUnknown()
		if knownAndSet(n.EndingSoonLeadSecs) {
			lead = n.EndingSoonLeadSecs.ValueInt64()
		}
		// A recurring full-day window is an hour short on a spring-forward day.
		maxLead := window
		fullDay := false
		if s.Repeat.ValueString() != "none" && !s.Repeat.IsNull() && window == 24*3600 {
			maxLead, fullDay = window-3600, true
		}
		if knownAndSet(n.Events) && leadKnown && setHasString(n.Events, "ending_soon") {
			switch {
			case lead < downtimeMinEndingSoonSec:
				diags.AddAttributeError(root.AtName("ending_soon_lead_secs"), "Reminder too early",
					"`ending_soon_lead_secs` must be at least 60 with the `ending_soon` event.")
			case windowKnown && lead > maxLead:
				extra := ""
				if fullDay {
					extra = " A full-day recurring window counts as an hour shorter, for the day the clocks go forward."
				}
				diags.AddAttributeError(root.AtName("ending_soon_lead_secs"), "Reminder longer than the window",
					fmt.Sprintf("`ending_soon_lead_secs` is %d (600 when unset), but it can be at most %d seconds "+
						"for this window.%s", lead, maxLead, extra))
			}
		}
	}
}

// validateDowntimeTargets mirrors the server's target rules.
func validateDowntimeTargets(targets []DowntimeTargetModel, diags *diag.Diagnostics) {
	if len(targets) == 0 {
		diags.AddAttributeError(path.Root("target"), "Missing target",
			"At least one `target` block is required: choose a module for the downtime to apply to.")
		return
	}
	seen := map[string]bool{}
	for i, t := range targets {
		p := path.Root("target").AtListIndex(i)
		if !knownAndSet(t.Module) {
			continue
		}
		module := t.Module.ValueString()
		if seen[module] {
			diags.AddAttributeError(p.AtName("module"), "Module chosen twice",
				fmt.Sprintf("The `%s` module has two target blocks. Use one target per module.", module))
		}
		seen[module] = true
		if knownAndSet(t.Tags) && module != "synthetics" {
			diags.AddAttributeError(p.AtName("tags"), "Tags narrow synthetics checks only",
				fmt.Sprintf("Remove `tags` from the `%s` target: only synthetics checks have tags to narrow by.", module))
		}
		if knownAndSet(t.SloMode) && module != "slos" {
			diags.AddAttributeError(p.AtName("slo_mode"), "slo_mode applies to SLOs only",
				fmt.Sprintf("Remove `slo_mode` from the `%s` target.", module))
		}
		if knownAndSet(t.IncidentMode) && t.IncidentMode.ValueString() != "muted" && module != "alerts" {
			diags.AddAttributeError(p.AtName("incident_mode"), "incident_mode applies to alerts only",
				fmt.Sprintf("Remove `incident_mode` from the `%s` target.", module))
		}
	}
}

// validateDowntimeSchedule mirrors the server's schedule rules and returns the
// window length in seconds, when the configuration decides it.
func validateDowntimeSchedule(s *DowntimeScheduleModel, diags *diag.Diagnostics) (int64, bool) {
	root := path.Root("schedule")
	if s.StartsAt.IsNull() {
		diags.AddAttributeError(root.AtName("starts_at"), "Missing starts_at", "`schedule.starts_at` is required.")
	}
	if s.Repeat.IsUnknown() {
		return 0, false
	}
	repeat := s.Repeat.ValueString()
	if s.Repeat.IsNull() {
		repeat = "none"
	}
	start, startOK := parseRFC3339(s.StartsAt)
	end, endOK := parseRFC3339(s.EndsAt)
	if startOK && endOK && !end.After(start) {
		diags.AddAttributeError(root.AtName("ends_at"), "End before start", "`ends_at` must be after `starts_at`.")
		return 0, false
	}

	if repeat == "none" {
		if s.EndsAt.IsNull() {
			diags.AddAttributeError(root.AtName("ends_at"), "Missing ends_at", "A one-time downtime needs `ends_at`.")
		}
		if knownAndSet(s.DurationSecs) {
			diags.AddAttributeError(root.AtName("duration_secs"), "duration_secs is set by the window",
				"A one-time downtime lasts from `starts_at` to `ends_at`, and the server sets `duration_secs` "+
					"to that span. Remove `duration_secs`.")
		}
		for _, f := range []struct {
			v    attr.Value
			name string
		}{{s.StartTimeLocal, "start_time_local"}, {s.Weekdays, "weekdays"}} {
			if knownAndSet(f.v) {
				diags.AddAttributeError(root.AtName(f.name), f.name+" applies to recurring downtimes only",
					fmt.Sprintf("Remove `%s`, or set `repeat` to `daily` or `weekly`.", f.name))
			}
		}
		if !startOK || !endOK {
			return 0, false
		}
		span := int64(end.Sub(start) / time.Second)
		if span < downtimeMinDurationSecs || span > downtimeMaxDurationSecs {
			diags.AddAttributeError(root.AtName("ends_at"), "Window out of range",
				fmt.Sprintf("The window lasts %d seconds. It must be between 1 minute and 7 days.", span))
			return 0, false
		}
		return span, true
	}

	if s.StartTimeLocal.IsNull() {
		diags.AddAttributeError(root.AtName("start_time_local"), "Missing start_time_local",
			"A recurring downtime needs `start_time_local`, the time each window starts, as HH:MM.")
	}
	if s.DurationSecs.IsNull() {
		diags.AddAttributeError(root.AtName("duration_secs"), "Missing duration_secs",
			"A recurring downtime needs `duration_secs`, how long each window lasts.")
	}
	var days []int64
	daysKnown := !s.Weekdays.IsUnknown()
	if knownAndSet(s.Weekdays) {
		for _, e := range s.Weekdays.Elements() {
			if v, ok := e.(types.Int64); ok && knownAndSet(v) {
				days = append(days, v.ValueInt64())
			} else {
				daysKnown = false
			}
		}
	}
	switch repeat {
	case "weekly":
		if s.Weekdays.IsNull() {
			diags.AddAttributeError(root.AtName("weekdays"), "Missing weekdays",
				"A weekly downtime needs `weekdays`, numbered 1 (Monday) to 7 (Sunday).")
		}
	case "daily":
		if knownAndSet(s.Weekdays) {
			diags.AddAttributeError(root.AtName("weekdays"), "weekdays applies to weekly downtimes only",
				"A daily downtime repeats every day. Remove `weekdays`, or set `repeat = \"weekly\"`.")
		}
	}
	if !knownAndSet(s.DurationSecs) {
		return 0, false
	}
	duration := s.DurationSecs.ValueInt64()
	gap := int64(24 * 3600)
	if repeat == "weekly" {
		if !daysKnown || len(days) == 0 {
			return duration, true
		}
		gap = weeklyGapSecs(days)
	}
	if duration > gap {
		diags.AddAttributeError(root.AtName("duration_secs"), "Window longer than the time between repeats",
			fmt.Sprintf("`duration_secs` is %d, but the windows start %d seconds apart, so they would overlap.",
				duration, gap))
	}
	return duration, true
}

// weeklyGapSecs is the shortest time between two window starts of a weekly
// schedule, the server's repeat_gap_secs.
func weeklyGapSecs(days []int64) int64 {
	d := append([]int64(nil), days...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	uniq := d[:0]
	for i, v := range d {
		if i == 0 || v != d[i-1] {
			uniq = append(uniq, v)
		}
	}
	minGap := uniq[0] + 7 - uniq[len(uniq)-1]
	for i := 1; i < len(uniq); i++ {
		if g := uniq[i] - uniq[i-1]; g < minGap {
			minGap = g
		}
	}
	return minGap * 24 * 3600
}

// ModifyPlan plans duration_secs of a one-time downtime as its span, the value
// the server stores.
func (r *DowntimeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan DowntimeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Schedule == nil || plan.Schedule.Repeat.ValueString() != "none" {
		return
	}
	start, ok1 := parseRFC3339(plan.Schedule.StartsAt)
	end, ok2 := parseRFC3339(plan.Schedule.EndsAt)
	if !ok1 || !ok2 {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("schedule").AtName("duration_secs"),
		types.Int64Value(int64(end.Sub(start)/time.Second)))...)
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func (r *DowntimeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DowntimeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := downtimeWriteFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := r.client.CreateDowntime(ctx, org, body)
	if err != nil {
		summary, detail := downtimeErrorDiag("creating", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	plan.OrgID = types.StringValue(org)
	plan.DowntimeID = types.StringValue(id)
	plan.ID = types.StringValue(downtimeResourceID(org, id))
	r.refresh(ctx, org, id, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		// The downtime exists. Record it, so Terraform marks it tainted and
		// replaces it, rather than leaving it on the server untracked.
		keepCreatedDowntime(ctx, &plan, resp)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// keepCreatedDowntime writes a created downtime to state when reading it back
// failed, with the values that are still unknown set to null.
func keepCreatedDowntime(ctx context.Context, plan *DowntimeResourceModel, resp *resource.CreateResponse) {
	if plan.Status.IsUnknown() {
		plan.Status = types.StringNull()
	}
	if plan.Schedule != nil && plan.Schedule.DurationSecs.IsUnknown() {
		plan.Schedule.DurationSecs = types.Int64Null()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *DowntimeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DowntimeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDowntime(ctx, org, state.DowntimeID.ValueString())
	if err != nil {
		summary, detail := downtimeErrorDiag("reading", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if d == nil {
		// The server's retention deletes ended and cancelled downtimes. Removing
		// one from state would make the next apply create it again, bringing a
		// cancelled window back, so it stays as last read.
		if downtimeFinished(state.Status.ValueString()) {
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(downtimeResourceID(org, d.ID))
	applyDowntimeToModel(ctx, d, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DowntimeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state DowntimeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	id := state.DowntimeID.ValueString()
	body := downtimeWriteFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The update replaces the quick-mute fields too, and it is refused when
	// the row changed since the version it names, so both come from the row
	// as it is now.
	current, err := r.client.GetDowntime(ctx, org, id)
	if err != nil {
		summary, detail := downtimeErrorDiag("reading", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if current == nil {
		detail := fmt.Sprintf("Downtime %q was deleted from org %q outside Terraform. Run `terraform apply` "+
			"again to create it anew.", id, org)
		if downtimeFinished(state.Status.ValueString()) {
			detail = fmt.Sprintf("Downtime %q had %s, and the server's retention has since deleted it, so it "+
				"cannot be changed. Replace it to schedule it again, for example with "+
				"`terraform apply -replace=<address>`.", id, state.Status.ValueString())
		}
		resp.Diagnostics.AddError("Downtime not found", detail)
		return
	}
	version := current.Version
	body.Version = &version
	body.AlertGroups = current.AlertGroups
	body.EndOnClear = current.EndOnClear
	// The update does not move a downtime; the move below does.
	body.FolderID = current.FolderID

	if err := r.client.UpdateDowntime(ctx, org, id, body); err != nil {
		summary, detail := downtimeErrorDiag("updating", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if folder := plan.FolderID.ValueString(); folder != "" && folder != current.FolderID {
		if err := r.client.MoveDowntime(ctx, org, id, folder); err != nil {
			summary, detail := downtimeErrorDiag("moving", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.DowntimeID = types.StringValue(id)
	plan.ID = types.StringValue(downtimeResourceID(org, id))
	r.refresh(ctx, org, id, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *DowntimeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DowntimeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.DowntimeID.ValueString()

	current, err := r.client.GetDowntime(ctx, org, id)
	if err != nil {
		summary, detail := downtimeErrorDiag("reading", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if current == nil {
		return
	}
	// The server refuses to delete a downtime that is active or scheduled.
	if current.Status == "active" || current.Status == "scheduled" {
		// A window that ended since the read cannot be cancelled any more, and
		// no longer needs to be.
		if err := r.client.CancelDowntime(ctx, org, id); err != nil && !isDowntimeAlreadyEnded(err) {
			summary, detail := downtimeErrorDiag("cancelling", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
	}
	if err := r.client.DeleteDowntime(ctx, org, id); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict &&
			strings.Contains(apiErr.Body, "corrected SLO slices") {
			resp.Diagnostics.AddWarning("Downtime cancelled but kept by the server",
				fmt.Sprintf("Downtime %q corrected SLO slices, so the server keeps it until the SLO window has "+
					"passed and its retention removes it. It is cancelled and no longer managed by Terraform.", id))
			return
		}
		summary, detail := downtimeErrorDiag("deleting", err)
		resp.Diagnostics.AddError(summary, detail)
	}
}

func (r *DowntimeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, id, found := strings.Cut(req.ID, "/")
	if !found {
		org, id = r.client.DefaultOrgID(), req.ID
	}
	if org == "" || id == "" || strings.Contains(id, "/") {
		resp.Diagnostics.AddError("Invalid import ID",
			"Import ID must be `{org_id}/{downtime_id}`, or a bare `{downtime_id}` for the provider's organization.")
		return
	}
	d, err := r.client.GetDowntime(ctx, org, id)
	if err != nil {
		summary, detail := downtimeErrorDiag("importing", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if d == nil {
		resp.Diagnostics.AddError("Downtime not found", fmt.Sprintf("Downtime %q not found in org %q.", id, org))
		return
	}
	state := DowntimeResourceModel{
		ID:         types.StringValue(downtimeResourceID(org, id)),
		DowntimeID: types.StringValue(id),
		OrgID:      types.StringValue(org),
		Targets:    types.ListNull(types.ObjectType{AttrTypes: downtimeTargetAttrTy}),
	}
	applyDowntimeToModel(ctx, d, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *DowntimeResource) refresh(ctx context.Context, org, id string, model *DowntimeResourceModel, diags *diag.Diagnostics) {
	d, err := r.client.GetDowntime(ctx, org, id)
	if err != nil {
		summary, detail := downtimeErrorDiag("reading", err)
		diags.AddError(summary, detail)
		return
	}
	if d == nil {
		diags.AddError("Downtime not found after write",
			fmt.Sprintf("Downtime %q was not found in org %q after being written.", id, org))
		return
	}
	applyDowntimeToModel(ctx, d, model, diags)
}

func downtimeResourceID(orgID, id string) string {
	return orgID + "/" + id
}

// ---------------------------------------------------------------------------
// Model <-> API
// ---------------------------------------------------------------------------

// downtimeTargets decodes the target blocks; known is false while the list or
// any of its modules is unknown, as with a dynamic block over an unresolved
// value.
func downtimeTargets(ctx context.Context, list types.List, diags *diag.Diagnostics) ([]DowntimeTargetModel, bool) {
	if list.IsUnknown() {
		return nil, false
	}
	if list.IsNull() {
		return nil, true
	}
	var out []DowntimeTargetModel
	diags.Append(list.ElementsAs(ctx, &out, false)...)
	for _, t := range out {
		if t.Module.IsUnknown() {
			return out, false
		}
	}
	return out, true
}

func downtimeWriteFromModel(ctx context.Context, m *DowntimeResourceModel, diags *diag.Diagnostics) downtimeWriteAPI {
	body := downtimeWriteAPI{
		FolderID:   m.FolderID.ValueString(),
		Name:       m.Name.ValueString(),
		Reason:     optString(m.Reason),
		ShowBanner: m.ShowBanner.ValueBool(),
		Targets:    []DowntimeTargetAPI{},
	}
	if knownAndSet(m.Condition) {
		cond, err := parseDowntimeCondition(m.Condition.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("condition"), "Invalid condition", err.Error())
			return body
		}
		raw, _ := json.Marshal(cond)
		body.Condition = raw
	}

	targets, _ := downtimeTargets(ctx, m.Targets, diags)
	for _, t := range targets {
		target := DowntimeTargetAPI{
			Module:       t.Module.ValueString(),
			Folders:      DowntimeTargetFoldersAPI{Kind: "all"},
			Tags:         stringsFromSet(ctx, t.Tags, diags),
			IDs:          stringsFromSet(ctx, t.IDs, diags),
			SloMode:      optString(t.SloMode),
			IncidentMode: t.IncidentMode.ValueString(),
		}
		if folders := stringsFromSet(ctx, t.FolderIDs, diags); len(folders) > 0 {
			target.Folders = DowntimeTargetFoldersAPI{Kind: "some", FolderIDs: folders}
		}
		body.Targets = append(body.Targets, target)
	}

	if s := m.Schedule; s != nil {
		sched := DowntimeScheduleAPI{
			Repeat:         s.Repeat.ValueString(),
			Timezone:       s.Timezone.ValueString(),
			StartTimeLocal: optString(s.StartTimeLocal),
			DurationSecs:   s.DurationSecs.ValueInt64(),
			Weekdays:       []int64{},
		}
		if start, ok := parseRFC3339(s.StartsAt); ok {
			sched.StartsAt = start.UnixMicro()
		}
		if end, ok := parseRFC3339(s.EndsAt); ok {
			micros := end.UnixMicro()
			sched.EndsAt = &micros
		}
		if knownAndSet(s.Weekdays) {
			var days []int64
			diags.Append(s.Weekdays.ElementsAs(ctx, &days, false)...)
			sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
			sched.Weekdays = days
		}
		body.Schedule = sched
	}

	if n := m.Notifications; n != nil {
		events := stringsFromSet(ctx, n.Events, diags)
		has := func(e string) bool {
			for _, v := range events {
				if v == e {
					return true
				}
			}
			return false
		}
		destinations := stringsFromSet(ctx, n.Destinations, diags)
		if destinations == nil {
			destinations = []string{}
		}
		body.Notifications = &DowntimeNotificationsAPI{
			Destinations: destinations,
			Events: DowntimeNotificationEventsAPI{
				Started:    has("started"),
				EndingSoon: has("ending_soon"),
				Ended:      has("ended"),
				Cancelled:  has("cancelled"),
				Extended:   has("extended"),
			},
			EndingSoonLeadSecs: n.EndingSoonLeadSecs.ValueInt64(),
		}
	}
	return body
}

func applyDowntimeToModel(ctx context.Context, d *DowntimeAPI, m *DowntimeResourceModel, diags *diag.Diagnostics) {
	m.DowntimeID = types.StringValue(d.ID)
	m.FolderID = types.StringValue(d.FolderID)
	m.Name = types.StringValue(d.Name)
	m.Reason = stringFromPtr(d.Reason)
	m.ShowBanner = types.BoolValue(d.ShowBanner)
	m.Status = types.StringValue(d.Status)
	m.Condition = downtimeConditionToModel(m.Condition, d.Condition, diags)

	targets := make([]DowntimeTargetModel, 0, len(d.Targets))
	for _, t := range d.Targets {
		target := DowntimeTargetModel{
			Module:       types.StringValue(t.Module),
			FolderIDs:    types.SetNull(types.StringType),
			Tags:         types.SetNull(types.StringType),
			IDs:          types.SetNull(types.StringType),
			SloMode:      stringFromPtr(t.SloMode),
			IncidentMode: types.StringValue("muted"),
		}
		if t.IncidentMode != "" {
			target.IncidentMode = types.StringValue(t.IncidentMode)
		}
		if t.Folders.Kind == "some" {
			target.FolderIDs = setFromStrings(ctx, t.Folders.FolderIDs, diags)
		}
		if len(t.Tags) > 0 {
			target.Tags = setFromStrings(ctx, t.Tags, diags)
		}
		if len(t.IDs) > 0 {
			target.IDs = setFromStrings(ctx, t.IDs, diags)
		}
		targets = append(targets, target)
	}
	list, d2 := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: downtimeTargetAttrTy}, targets)
	diags.Append(d2...)
	m.Targets = list

	var prior DowntimeScheduleModel
	if m.Schedule != nil {
		prior = *m.Schedule
	}
	s := d.Schedule
	sched := &DowntimeScheduleModel{
		Repeat:         types.StringValue(s.Repeat),
		StartsAt:       timestampToModel(prior.StartsAt, s.StartsAt),
		EndsAt:         types.StringNull(),
		Timezone:       types.StringValue(s.Timezone),
		StartTimeLocal: stringFromPtr(s.StartTimeLocal),
		DurationSecs:   types.Int64Value(s.DurationSecs),
		Weekdays:       types.SetNull(types.Int64Type),
	}
	if s.EndsAt != nil {
		sched.EndsAt = timestampToModel(prior.EndsAt, *s.EndsAt)
	}
	if len(s.Weekdays) > 0 {
		set, d3 := types.SetValueFrom(ctx, types.Int64Type, s.Weekdays)
		diags.Append(d3...)
		sched.Weekdays = set
	}
	m.Schedule = sched

	m.Notifications = nil
	if n := d.Notifications; n != nil && len(n.Destinations) > 0 {
		var events []string
		for _, e := range []struct {
			on   bool
			name string
		}{
			{n.Events.Started, "started"}, {n.Events.EndingSoon, "ending_soon"}, {n.Events.Ended, "ended"},
			{n.Events.Cancelled, "cancelled"}, {n.Events.Extended, "extended"},
		} {
			if e.on {
				events = append(events, e.name)
			}
		}
		notifications := &DowntimeNotificationsModel{
			Destinations:       setFromStrings(ctx, n.Destinations, diags),
			Events:             types.SetNull(types.StringType),
			EndingSoonLeadSecs: types.Int64Value(n.EndingSoonLeadSecs),
		}
		if len(events) > 0 {
			notifications.Events = setFromStrings(ctx, events, diags)
		}
		m.Notifications = notifications
	}
}

// ---------------------------------------------------------------------------
// Timestamps
// ---------------------------------------------------------------------------

func parseRFC3339(v types.String) (time.Time, bool) {
	if !knownAndSet(v) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v.ValueString())
	return t, err == nil
}

// timestampToModel renders the server's microseconds as RFC 3339 in UTC,
// keeping the prior string when it names the same instant, so that a
// configuration written with an offset, such as +05:30, does not show a diff.
func timestampToModel(prior types.String, micros int64) types.String {
	if t, ok := parseRFC3339(prior); ok && t.UnixMicro() == micros {
		return prior
	}
	return types.StringValue(time.UnixMicro(micros).UTC().Format(time.RFC3339Nano))
}

type rfc3339Validator struct{}

func (rfc3339Validator) Description(context.Context) string {
	return "must be an RFC 3339 timestamp, such as 2026-12-01T22:00:00Z"
}

func (v rfc3339Validator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (rfc3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !knownAndSet(req.ConfigValue) {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid timestamp",
			fmt.Sprintf("%q is not an RFC 3339 timestamp, such as 2026-12-01T22:00:00Z or "+
				"2026-12-01T23:00:00+01:00.", req.ConfigValue.ValueString()))
	}
}

// ---------------------------------------------------------------------------
// Condition
// ---------------------------------------------------------------------------

// downtimeCondition is a dimension condition: a pair or a group of them.
type downtimeCondition struct {
	Type     string              `json:"type"`
	Op       string              `json:"op,omitempty"`
	Items    []downtimeCondition `json:"items,omitempty"`
	Key      string              `json:"key,omitempty"`
	Operator string              `json:"operator,omitempty"`
	Value    *string             `json:"value,omitempty"`
}

// parseDowntimeCondition checks a condition's shape. The server checks the
// rest: the keys, the depth and that every branch has an `=` pair.
func parseDowntimeCondition(raw string) (*downtimeCondition, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var c downtimeCondition
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("the condition is not a valid condition document: %v", err)
	}
	if err := checkDowntimeCondition(&c, "condition"); err != nil {
		return nil, err
	}
	return &c, nil
}

func checkDowntimeCondition(c *downtimeCondition, at string) error {
	switch c.Type {
	case "pair":
		if strings.TrimSpace(c.Key) == "" {
			return fmt.Errorf("%s is a pair with no `key`", at)
		}
		if c.Key != strings.TrimSpace(c.Key) {
			return fmt.Errorf("%s has key %q with surrounding spaces; the server refuses a key that is not "+
				"exactly a dimension of the organization", at, c.Key)
		}
		if c.Operator != "=" && c.Operator != "!=" {
			return fmt.Errorf("%s has operator %q; it must be `=` or `!=`", at, c.Operator)
		}
		if c.Value == nil {
			return fmt.Errorf("%s is a pair with no `value`", at)
		}
		if c.Op != "" || c.Items != nil {
			return fmt.Errorf("%s is a pair, which takes `key`, `operator` and `value` only", at)
		}
	case "group":
		if c.Op != "and" && c.Op != "or" {
			return fmt.Errorf("%s is a group with op %q; it must be `and` or `or`", at, c.Op)
		}
		if len(c.Items) == 0 {
			return fmt.Errorf("%s is a group with no `items`", at)
		}
		if c.Key != "" || c.Operator != "" || c.Value != nil {
			return fmt.Errorf("%s is a group, which takes `op` and `items` only", at)
		}
		for i := range c.Items {
			if err := checkDowntimeCondition(&c.Items[i], fmt.Sprintf("%s.items[%d]", at, i)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s has type %q; it must be `pair` or `group`", at, c.Type)
	}
	return nil
}

// normalizedDowntimeCondition is the condition the way the server stores it:
// keys trimmed, values trimmed and lowercased.
func normalizedDowntimeCondition(c downtimeCondition) downtimeCondition {
	if c.Type == "pair" {
		c.Key = strings.TrimSpace(c.Key)
		if c.Value != nil {
			v := strings.ToLower(strings.TrimSpace(*c.Value))
			c.Value = &v
		}
		return c
	}
	items := make([]downtimeCondition, len(c.Items))
	for i, item := range c.Items {
		items[i] = normalizedDowntimeCondition(item)
	}
	c.Items = items
	return c
}

// downtimeConditionToModel keeps the configured condition when the server's
// is the same condition after the server's normalization, so that formatting
// and the casing of values do not show as a diff.
func downtimeConditionToModel(prior types.String, raw json.RawMessage, diags *diag.Diagnostics) types.String {
	if len(raw) == 0 || string(raw) == "null" {
		return types.StringNull()
	}
	var server downtimeCondition
	if err := json.Unmarshal(raw, &server); err != nil {
		diags.AddWarning("Unreadable downtime condition",
			fmt.Sprintf("The server's condition could not be read: %v", err))
		return types.StringValue(string(raw))
	}
	if knownAndSet(prior) {
		if mine, err := parseDowntimeCondition(prior.ValueString()); err == nil &&
			sameDowntimeCondition(normalizedDowntimeCondition(*mine), normalizedDowntimeCondition(server)) {
			return prior
		}
	}
	// The form jsonencode() writes: compact, keys sorted. A configuration
	// written with jsonencode() then matches it exactly after an import.
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return types.StringValue(string(raw))
	}
	out, _ := json.Marshal(generic)
	return types.StringValue(string(out))
}

func anyModuleWithDimensions(targets []DowntimeTargetModel) bool {
	for _, t := range targets {
		if !t.Module.IsUnknown() && t.Module.ValueString() != "synthetics" {
			return true
		}
	}
	return false
}

func setHasString(s types.Set, want string) bool {
	for _, e := range s.Elements() {
		if v, ok := e.(types.String); ok && v.ValueString() == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// String validators
// ---------------------------------------------------------------------------

// trimmedStringValidator refuses leading and trailing whitespace, which the
// server trims, so the stored value would differ from the configured one.
type trimmedStringValidator struct{}

func (trimmedStringValidator) Description(context.Context) string {
	return "must not start or end with whitespace"
}

func (v trimmedStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (trimmedStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !knownAndSet(req.ConfigValue) {
		return
	}
	if s := req.ConfigValue.ValueString(); s != strings.TrimSpace(s) || strings.TrimSpace(s) == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Leading or trailing whitespace",
			"The server trims this value, so it must not start or end with whitespace, nor be blank.")
	}
}

// lowercaseTrimmedValidator refuses values the server would lowercase or trim.
type lowercaseTrimmedValidator struct{}

func (lowercaseTrimmedValidator) Description(context.Context) string {
	return "must be lowercase, with no leading or trailing whitespace"
}

func (v lowercaseTrimmedValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (lowercaseTrimmedValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !knownAndSet(req.ConfigValue) {
		return
	}
	s := req.ConfigValue.ValueString()
	if s != strings.ToLower(strings.TrimSpace(s)) || s == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Tag must be lowercase",
			fmt.Sprintf("%q: the server stores tags trimmed and lowercased, so write it as %q.",
				s, strings.ToLower(strings.TrimSpace(s))))
	}
}

// sameDowntimeCondition compares two normalized conditions. Values are
// compared case-insensitively too: the server lowercases with Rust's
// to_lowercase, which differs from Go's for a few characters (a final sigma,
// a dotted capital I), and a value the server lowercased its way is still the
// value the configuration wrote.
func sameDowntimeCondition(a, b downtimeCondition) bool {
	if a.Type != b.Type || a.Op != b.Op || a.Key != b.Key || a.Operator != b.Operator ||
		len(a.Items) != len(b.Items) || (a.Value == nil) != (b.Value == nil) {
		return false
	}
	if a.Value != nil && *a.Value != *b.Value {
		strip := func(s string) string { return strings.ReplaceAll(s, "\u0307", "") }
		if !strings.EqualFold(strip(*a.Value), strip(*b.Value)) {
			return false
		}
	}
	for i := range a.Items {
		if !sameDowntimeCondition(a.Items[i], b.Items[i]) {
			return false
		}
	}
	return true
}

// downtimeFinished reports whether a status is one the server's retention
// eventually deletes.
func downtimeFinished(status string) bool {
	return status == "ended" || status == "cancelled" || status == "ended_early"
}

// isDowntimeAlreadyEnded reports the 409 a cancel gets for a window that has
// already ended.
func isDowntimeAlreadyEnded(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict &&
		strings.Contains(apiErr.Body, "already ended")
}
