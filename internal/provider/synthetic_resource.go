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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &SyntheticResource{}
	_ resource.ResourceWithConfigure      = &SyntheticResource{}
	_ resource.ResourceWithImportState    = &SyntheticResource{}
	_ resource.ResourceWithValidateConfig = &SyntheticResource{}
)

// maxBrowserRetries mirrors the server's ceiling for browser checks. A browser
// run costs devices times attempts times journey budget, and at three retries a
// long journey already exceeds the probe's function timeout, so the check would
// validate and then be killed mid-journey, reporting a failure the target never
// had.
const maxBrowserRetries = 2

// NewSyntheticResource returns a factory for the openobserve_synthetic resource.
func NewSyntheticResource() resource.Resource { return &SyntheticResource{} }

// SyntheticResource manages a synthetic check.
type SyntheticResource struct{ client *Client }

// SyntheticResourceModel holds the Terraform state for a synthetic check.
type SyntheticResourceModel struct {
	ID          types.String `tfsdk:"id"`
	SyntheticID types.String `tfsdk:"synthetic_id"`
	OrgID       types.String `tfsdk:"org_id"`
	FolderID    types.String `tfsdk:"folder_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Tags        types.Set    `tfsdk:"tags"`
	Type        types.String `tfsdk:"type"`
	Target      types.String `tfsdk:"target"`
	Config      types.String `tfsdk:"config"`

	FrequencyType     types.String `tfsdk:"frequency_type"`
	FrequencyInterval types.Int64  `tfsdk:"frequency_interval"`
	Cron              types.String `tfsdk:"cron"`
	Timezone          types.String `tfsdk:"timezone"`
	TZOffset          types.Int64  `tfsdk:"tz_offset"`

	Locations    types.List `tfsdk:"locations"`
	Enabled      types.Bool `tfsdk:"enabled"`
	Destinations types.Set  `tfsdk:"destinations"`

	Retries             types.Int64 `tfsdk:"retries"`
	WaitBeforeRetrySecs types.Int64 `tfsdk:"wait_before_retry_secs"`
	AlertIfFails        types.Int64 `tfsdk:"alert_if_fails"`
	CooldownMins        types.Int64 `tfsdk:"cooldown_mins"`

	CollectRUMData types.Bool `tfsdk:"collect_rum_data"`
	SessionReplay  types.Bool `tfsdk:"session_replay"`

	Auth      *SyntheticAuthModel      `tfsdk:"auth"`
	Cookies   []SyntheticCookieModel   `tfsdk:"cookie"`
	Variables []SyntheticVariableModel `tfsdk:"variable"`
}

// SyntheticAuthModel is the auth block. Flat with a `type` discriminator, so a
// `dynamic` block over it stays usable.
type SyntheticAuthModel struct {
	Type       types.String `tfsdk:"type"`
	Username   types.String `tfsdk:"username"`
	Password   types.String `tfsdk:"password"`
	Token      types.String `tfsdk:"token"`
	SecretName types.String `tfsdk:"secret_name"`
}

// SyntheticCookieModel is one cookie block.
type SyntheticCookieModel struct {
	Name     types.String `tfsdk:"name"`
	Value    types.String `tfsdk:"value"`
	Domain   types.String `tfsdk:"domain"`
	Path     types.String `tfsdk:"path"`
	HTTPOnly types.Bool   `tfsdk:"http_only"`
	Secure   types.Bool   `tfsdk:"secure"`
}

// SyntheticVariableModel is one variable block.
type SyntheticVariableModel struct {
	Name    types.String `tfsdk:"name"`
	Value   types.String `tfsdk:"value"`
	Secure  types.Bool   `tfsdk:"secure"`
	Example types.String `tfsdk:"example"`
}

func (r *SyntheticResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_synthetic"
}

func (r *SyntheticResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve synthetic check.\n\n" +
			"A synthetic check probes a target from one or more locations on a schedule and notifies alert " +
			"destinations when it fails. Unlike an alert, which watches data you already have, a synthetic " +
			"produces the data: it tells you the service is unreachable from Frankfurt even when nobody has " +
			"complained yet.\n\n" +
			"Five kinds: `http`, `tcp`, `tls` (certificate expiry and chain validity), `ssh`, and `browser` " +
			"(a scripted journey).\n\n" +
			"~> Synthetics are gated behind `ZO_SYNTHETICS_ENABLED` on the server. When it is off the routes " +
			"are not registered at all, so the provider reports that the feature is disabled rather than " +
			"passing a bare 404 through.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{synthetic_id}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"synthetic_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned check identifier.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the check belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"folder_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("default"),
				Description: "Folder holding this check. Use `openobserve_folder` with " +
					"`folder_type = \"synthetics\"`.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Check name, unique within the organization.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "What this check verifies.",
			},
			"tags": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Selection tags, for example `prod` or `checkout`.",
			},
			"type": schema.StringAttribute{
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf(SyntheticTypes...)},
				Description: "What kind of probe to run:\n\n" +
					"- `http`: request a URL and assert on the response\n" +
					"- `tcp`: open a socket\n" +
					"- `tls`: check certificate expiry, chain and hostname\n" +
					"- `ssh`: connect over SSH\n" +
					"- `browser`: run a scripted journey in a real browser",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"target": schema.StringAttribute{
				Required: true,
				Description: "What to probe. A URL for `http` and `browser`, `host:port` for `tcp`, `tls` " +
					"and `ssh`.",
			},
			"config": schema.StringAttribute{
				Optional: true,
				Description: "Type-specific settings as a JSON document, built with `jsonencode()`.\n\n" +
					"The shape depends on `type`, which is why this is a document rather than a set of " +
					"attributes: modelling one check kind would misrepresent the other four. Expected " +
					"headers, assertions, timeouts and browser steps all live here.\n\n" +
					"The fastest way to get it right is to build the check once in the UI and read it back " +
					"with the `openobserve_synthetics` data source.",
			},

			"frequency_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("minutes"),
				Validators:  []validator.String{stringvalidator.OneOf(SyntheticFrequencyTypes...)},
				Description: "Schedule unit: `seconds`, `minutes`, `hours`, `days`, `weeks`, `months`, or `cron`.",
			},
			"frequency_interval": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(5),
				Description: "How many `frequency_type` units between runs. Ignored when `frequency_type` is `cron`.",
			},
			"cron": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Cron expression, required when `frequency_type` is `cron`.",
			},
			"timezone": schema.StringAttribute{
				Optional:    true,
				Description: "IANA timezone for cron scheduling, for example `America/New_York`.",
			},
			"tz_offset": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Timezone offset in minutes from UTC, negative west of it.",
			},

			"locations": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Which locations to probe from, by **identifier** rather than label.\n\n" +
					"Read them from the `openobserve_synthetic_locations` data source. Locations are " +
					"registered out of band, by running a probe agent or by OpenObserve for its public " +
					"regions, so a deployment with none registered cannot run any check:\n\n" +
					"    no locations are registered on this deployment, register at least one location " +
					"before creating synthetics\n\n" +
					"A private location also reads `status = \"pending\"` until an agent checks in, and a " +
					"check assigned to it will not run even though it applies cleanly.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the check runs. A disabled check stays configured but idle.",
			},
			"destinations": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Names of the `openobserve_alert_destination` resources notified when the check fails.",
			},

			"retries": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(0),
				Description: "Retries before a run counts as failed. Zero means a single attempt.\n\n" +
					"Capped at 2 for `browser` checks, because a browser run costs devices times attempts " +
					"times journey budget and a longer journey would be killed by the probe timeout, " +
					"reporting a failure the target never had.",
			},
			"wait_before_retry_secs": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(5),
				Description: "Seconds to wait between retry attempts, 0 to 300. Defaults to 5, matching the " +
					"server.\n\n" +
					"The wait counts against the check budget, a fixed ceiling on a single run set by " +
					"`ZO_SYNTHETICS_MAX_CHECK_BUDGET_SECS` (840 seconds by default). A check whose worst case " +
					"exceeds it is rejected at write time, so raising this alongside `retries` can make an " +
					"otherwise valid check unsavable.",
			},
			"alert_if_fails": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(1),
				Description: "Notify only after this many consecutive failed runs. Raising it rides out a " +
					"single flaky probe, at the cost of finding out later.",
			},
			"cooldown_mins": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
				Description: "Minutes of silence between repeated notifications for the same failing check.",
			},

			"collect_rum_data": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Collect real user monitoring data during a `browser` run: performance timings and more.",
			},
			"session_replay": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Capture a session replay of a `browser` run. Browser checks only.",
			},
		},
		Blocks: map[string]schema.Block{
			"auth": schema.SingleNestedBlock{
				Description: "How to authenticate against the target.",
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Optional:    true,
						Validators:  []validator.String{stringvalidator.OneOf(SyntheticAuthTypes...)},
						Description: "`basic`, `bearer`, or `secret` to reference a stored secret.",
					},
					"username": schema.StringAttribute{
						Optional:    true,
						Description: "Username, for `basic`.",
					},
					"password": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "Password, for `basic`. Stored in Terraform state.",
					},
					"token": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "Bearer token, for `bearer`. Stored in Terraform state.",
					},
					"secret_name": schema.StringAttribute{
						Optional: true,
						Description: "Name of a secret held by OpenObserve, for `secret`. Preferable to the " +
							"other two: the credential never enters Terraform state.",
					},
				},
			},
			"cookie": schema.ListNestedBlock{
				Description: "Cookies injected into the browser context before any step runs. Browser checks " +
					"only, and independent of `auth`, so the two can be combined.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name":   schema.StringAttribute{Required: true, Description: "Cookie name."},
						"value":  schema.StringAttribute{Required: true, Sensitive: true, Description: "Cookie value. Encrypted at rest, and stored in Terraform state."},
						"domain": schema.StringAttribute{Required: true, Description: "Domain the cookie applies to."},
						"path": schema.StringAttribute{
							Optional: true, Computed: true,
							Default:     stringdefault.StaticString("/"),
							Description: "Path the cookie applies to.",
						},
						"http_only": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Set the HttpOnly flag."},
						"secure":    schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Set the Secure flag."},
					},
				},
			},
			"variable": schema.ListNestedBlock{
				Description: "Key/value variables injected into the probe environment, for parameterising a " +
					"check across environments.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name":  schema.StringAttribute{Required: true, Description: "Variable name."},
						"value": schema.StringAttribute{Required: true, Sensitive: true, Description: "Variable value. Encrypted at rest regardless of `secure`, and stored in Terraform state."},
						"secure": schema.BoolAttribute{
							Optional: true, Computed: true, Default: booldefault.StaticBool(false),
							Description: "Mask the value in the UI. Display only: every value is encrypted at " +
								"rest either way.",
						},
						"example": schema.StringAttribute{
							Optional:    true,
							Description: "Placeholder shown in the UI when `secure` is set and the value is hidden.",
						},
					},
				},
			},
		},
	}
}

// ValidateConfig reports the combinations the server rejects, and the ones it
// accepts but silently ignores.
func (r *SyntheticResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config SyntheticResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.FrequencyType.ValueString() == "cron" &&
		config.Cron.IsNull() && !config.Cron.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("cron"),
			"Missing cron",
			"`cron` is required when `frequency_type` is `cron`.",
		)
	}

	checkType := config.Type.ValueString()

	if knownAndSet(config.Retries) && checkType == "browser" &&
		config.Retries.ValueInt64() > maxBrowserRetries {
		resp.Diagnostics.AddAttributeError(
			path.Root("retries"),
			"Too many retries for a browser check",
			fmt.Sprintf("A browser check allows at most %d retries. A browser run costs devices times "+
				"attempts times journey budget, so more would be killed by the probe timeout and reported "+
				"as a failure the target never had.", maxBrowserRetries),
		)
	}

	// These two only mean anything for a browser journey. The server ignores
	// them elsewhere, which is worse than rejecting them: the configuration
	// reads as if replay were being captured.
	if checkType != "" && checkType != "browser" {
		if knownAndSet(config.SessionReplay) && config.SessionReplay.ValueBool() {
			resp.Diagnostics.AddAttributeError(
				path.Root("session_replay"),
				"Session replay applies only to browser checks",
				fmt.Sprintf("`session_replay` has no effect on a %q check. Remove it, or change `type` to "+
					"`browser`.", checkType),
			)
		}
		if knownAndSet(config.CollectRUMData) && config.CollectRUMData.ValueBool() {
			resp.Diagnostics.AddAttributeError(
				path.Root("collect_rum_data"),
				"RUM collection applies only to browser checks",
				fmt.Sprintf("`collect_rum_data` has no effect on a %q check. Remove it, or change `type` to "+
					"`browser`.", checkType),
			)
		}
	}

	// The auth block is flat, so a type says which field is expected.
	if config.Auth != nil && knownAndSet(config.Auth.Type) {
		switch config.Auth.Type.ValueString() {
		case "basic":
			if config.Auth.Username.IsNull() && !config.Auth.Username.IsUnknown() {
				resp.Diagnostics.AddAttributeError(path.Root("auth").AtName("username"),
					"Missing auth.username", "`username` is required when `auth.type` is `basic`.")
			}
			if config.Auth.Password.IsNull() && !config.Auth.Password.IsUnknown() {
				resp.Diagnostics.AddAttributeError(path.Root("auth").AtName("password"),
					"Missing auth.password", "`password` is required when `auth.type` is `basic`.")
			}
		case "bearer":
			if config.Auth.Token.IsNull() && !config.Auth.Token.IsUnknown() {
				resp.Diagnostics.AddAttributeError(path.Root("auth").AtName("token"),
					"Missing auth.token", "`token` is required when `auth.type` is `bearer`.")
			}
		case "secret":
			if config.Auth.SecretName.IsNull() && !config.Auth.SecretName.IsUnknown() {
				resp.Diagnostics.AddAttributeError(path.Root("auth").AtName("secret_name"),
					"Missing auth.secret_name", "`secret_name` is required when `auth.type` is `secret`.")
			}
		}
	}
}

func (r *SyntheticResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

// apiFromModel renders the check. folderID is carried in the body because the
// update endpoint reads it from there, and writing an empty one violates a
// foreign key: the server answers `(code: 787) FOREIGN KEY constraint failed`
// with no hint that a folder was what it wanted. Create ignores the body's
// folder and takes the query parameter instead.
func (r *SyntheticResource) apiFromModel(ctx context.Context, model *SyntheticResourceModel, org, folderID string, diags *diag.Diagnostics) SyntheticAPI {
	out := SyntheticAPI{
		OrgID:       org,
		FolderID:    folderID,
		TZOffset:    int32(model.TZOffset.ValueInt64()),
		Name:        model.Name.ValueString(),
		Description: model.Description.ValueString(),
		Tags:        stringsFromSet(ctx, model.Tags, diags),
		CheckType:   model.Type.ValueString(),
		Target:      model.Target.ValueString(),
		Frequency: SyntheticFrequencyAPI{
			FrequencyType: model.FrequencyType.ValueString(),
			Interval:      model.FrequencyInterval.ValueInt64(),
			Cron:          model.Cron.ValueString(),
			Timezone:      optString(model.Timezone),
		},
		Locations:           stringsFromList(ctx, model.Locations, diags),
		Enabled:             model.Enabled.ValueBool(),
		Destinations:        stringsFromSet(ctx, model.Destinations, diags),
		Retries:             int32(model.Retries.ValueInt64()),
		WaitBeforeRetrySecs: int32(model.WaitBeforeRetrySecs.ValueInt64()),
		AlertIfFails:        int32(model.AlertIfFails.ValueInt64()),
		CooldownMins:        int32(model.CooldownMins.ValueInt64()),
		CollectRUMData:      model.CollectRUMData.ValueBool(),
		SessionReplay:       model.SessionReplay.ValueBool(),
	}

	// Config is required on the wire, so an omitted one becomes an empty
	// object rather than null.
	if knownAndSet(model.Config) {
		out.Config = []byte(model.Config.ValueString())
	} else {
		out.Config = []byte("{}")
	}

	if model.Auth != nil && knownAndSet(model.Auth.Type) {
		out.Auth = &SyntheticAuthAPI{
			AuthType:   model.Auth.Type.ValueString(),
			Username:   model.Auth.Username.ValueString(),
			Password:   model.Auth.Password.ValueString(),
			Token:      model.Auth.Token.ValueString(),
			SecretName: model.Auth.SecretName.ValueString(),
		}
	}
	for _, c := range model.Cookies {
		out.Cookies = append(out.Cookies, SyntheticCookieAPI{
			Name:     c.Name.ValueString(),
			Value:    c.Value.ValueString(),
			Domain:   c.Domain.ValueString(),
			Path:     c.Path.ValueString(),
			HTTPOnly: c.HTTPOnly.ValueBool(),
			Secure:   c.Secure.ValueBool(),
		})
	}
	for _, v := range model.Variables {
		out.Variables = append(out.Variables, SyntheticVariableAPI{
			Name:    v.Name.ValueString(),
			Value:   v.Value.ValueString(),
			Secure:  v.Secure.ValueBool(),
			Example: v.Example.ValueString(),
		})
	}
	return out
}

func (r *SyntheticResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SyntheticResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	folderID := plan.FolderID.ValueString()
	body := r.apiFromModel(ctx, &plan, org, folderID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := r.client.CreateSynthetic(ctx, org, folderID, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating synthetic check", syntheticErrorDetail(err))
		return
	}
	if id == "" {
		found, lookupErr := r.client.FindSyntheticByName(ctx, org, plan.Name.ValueString())
		if lookupErr != nil || found == nil {
			resp.Diagnostics.AddError("Error resolving synthetic check after create",
				"The check was created but the server did not return its ID, and looking it up by name failed.")
			return
		}
		id = found.ID
	}

	plan.OrgID = types.StringValue(org)
	plan.SyntheticID = types.StringValue(id)
	plan.ID = types.StringValue(functionResourceID(org, id))
	r.refresh(ctx, org, id, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SyntheticResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SyntheticResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	check, err := r.client.GetSynthetic(ctx, org, state.SyntheticID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading synthetic check", syntheticErrorDetail(err))
		return
	}
	if check == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(functionResourceID(org, check.ID))
	r.applyToModel(ctx, check, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SyntheticResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SyntheticResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	id := state.SyntheticID.ValueString()
	folderID := plan.FolderID.ValueString()
	// The update body must carry the folder the check is in now. A folder
	// change is a separate move below, so the current one is what goes here.
	bodyFolder := state.FolderID.ValueString()
	if bodyFolder == "" {
		bodyFolder = folderID
	}
	body := r.apiFromModel(ctx, &plan, org, bodyFolder, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	body.ID = id

	if err := r.client.UpdateSynthetic(ctx, org, id, body); err != nil {
		resp.Diagnostics.AddError("Error updating synthetic check", syntheticErrorDetail(err))
		return
	}

	// Enabling and moving are separate endpoints, as with alerts, so a change
	// to either is its own call.
	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if err := r.client.SetSyntheticEnabled(ctx, org, id, plan.Enabled.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error changing synthetic enabled state", syntheticErrorDetail(err))
			return
		}
	}
	if folderID != "" && folderID != state.FolderID.ValueString() {
		if err := r.client.MoveSynthetics(ctx, org, folderID, []string{id}); err != nil {
			resp.Diagnostics.AddError("Error moving synthetic check between folders", syntheticErrorDetail(err))
			return
		}
	}

	plan.OrgID = types.StringValue(org)
	plan.SyntheticID = types.StringValue(id)
	plan.ID = types.StringValue(functionResourceID(org, id))
	r.refresh(ctx, org, id, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SyntheticResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SyntheticResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSynthetic(ctx, org, state.SyntheticID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting synthetic check", syntheticErrorDetail(err))
	}
}

// ImportState supports: terraform import openobserve_synthetic.example default/2abcXYZ
func (r *SyntheticResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{synthetic_id}`, for example `default/2abcXYZ`.",
		)
		return
	}
	check, err := r.client.GetSynthetic(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading synthetic check during import", syntheticErrorDetail(err))
		return
	}
	if check == nil {
		resp.Diagnostics.AddError("Synthetic check not found",
			fmt.Sprintf("Synthetic check %q not found in org %q.", parts[1], parts[0]))
		return
	}
	state := SyntheticResourceModel{
		ID:          types.StringValue(req.ID),
		OrgID:       types.StringValue(parts[0]),
		SyntheticID: types.StringValue(parts[1]),
	}
	r.applyToModel(ctx, check, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SyntheticResource) refresh(ctx context.Context, org, id string, model *SyntheticResourceModel, diags *diag.Diagnostics) {
	check, err := r.client.GetSynthetic(ctx, org, id)
	if err != nil {
		diags.AddError("Error reading synthetic check after write", syntheticErrorDetail(err))
		return
	}
	if check == nil {
		diags.AddError("Synthetic check not found after write",
			fmt.Sprintf("Synthetic check %q was not found in org %q after being written.", id, org))
		return
	}
	r.applyToModel(ctx, check, model, diags)
}

func (r *SyntheticResource) applyToModel(ctx context.Context, api *SyntheticAPI, model *SyntheticResourceModel, diags *diag.Diagnostics) {
	model.SyntheticID = types.StringValue(api.ID)
	model.Name = types.StringValue(api.Name)
	model.Description = types.StringValue(api.Description)
	model.Type = types.StringValue(api.CheckType)
	model.Target = types.StringValue(api.Target)
	model.TZOffset = types.Int64Value(int64(api.TZOffset))
	model.Enabled = types.BoolValue(api.Enabled)
	model.Retries = types.Int64Value(int64(api.Retries))
	model.WaitBeforeRetrySecs = types.Int64Value(int64(api.WaitBeforeRetrySecs))
	model.AlertIfFails = types.Int64Value(int64(api.AlertIfFails))
	model.CooldownMins = types.Int64Value(int64(api.CooldownMins))
	model.CollectRUMData = types.BoolValue(api.CollectRUMData)
	model.SessionReplay = types.BoolValue(api.SessionReplay)

	model.FrequencyType = types.StringValue(api.Frequency.FrequencyType)
	model.FrequencyInterval = types.Int64Value(api.Frequency.Interval)
	model.Cron = types.StringValue(api.Frequency.Cron)
	model.Timezone = stringFromPtr(api.Frequency.Timezone)

	// The server reports a folder as the id it resolved on create, but echoes
	// whatever spelling an update sent. Both name the same folder, so the
	// configured value is kept unless it was never set.
	if api.FolderID != "" && model.FolderID.IsNull() {
		model.FolderID = types.StringValue(api.FolderID)
	}

	// The config document is compared as JSON rather than as text, because the
	// server reorders keys and adds defaults of its own.
	//
	// A check with no type-specific settings is sent as `{}`, because the field
	// is not optional on the wire, and comes back the same way. Writing that
	// back where the configuration said nothing would be an inconsistent result
	// after apply, so an empty object stays null.
	switch {
	case len(api.Config) == 0:
		// leave whatever the model already holds
	case model.Config.IsNull() && isEmptyJSONObject(api.Config):
		model.Config = types.StringNull()
	default:
		model.Config = reconcileJSON(model.Config, api.Config, diags)
	}

	model.Locations = listFromStrings(ctx, api.Locations, diags)
	if len(api.Tags) == 0 {
		model.Tags = types.SetNull(types.StringType)
	} else {
		model.Tags = setFromStrings(ctx, api.Tags, diags)
	}
	if len(api.Destinations) == 0 {
		model.Destinations = types.SetNull(types.StringType)
	} else {
		model.Destinations = setFromStrings(ctx, api.Destinations, diags)
	}

	// Credentials are the one thing not taken from the server. A check read
	// back may carry them redacted, and overwriting the configured value with
	// a redaction would both lose it and show as drift on every plan.
	//
	// The same reasoning covers cookies and variables, which also carry
	// secrets: a variable marked secure comes back with its value emptied and
	// only `example` populated. So each is filled in from the server only when
	// the model has none, which is the import case. Once a configuration owns
	// them, the configured values stand.
	if api.Auth != nil && model.Auth == nil {
		model.Auth = &SyntheticAuthModel{
			Type:       types.StringValue(api.Auth.AuthType),
			Username:   stringOrNull(api.Auth.Username),
			Password:   stringOrNull(api.Auth.Password),
			Token:      stringOrNull(api.Auth.Token),
			SecretName: stringOrNull(api.Auth.SecretName),
		}
	}

	if len(model.Cookies) == 0 {
		for _, c := range api.Cookies {
			model.Cookies = append(model.Cookies, SyntheticCookieModel{
				Name:     types.StringValue(c.Name),
				Value:    types.StringValue(c.Value),
				Domain:   types.StringValue(c.Domain),
				Path:     types.StringValue(c.Path),
				HTTPOnly: types.BoolValue(c.HTTPOnly),
				Secure:   types.BoolValue(c.Secure),
			})
		}
	}

	if len(model.Variables) == 0 {
		for _, v := range api.Variables {
			model.Variables = append(model.Variables, SyntheticVariableModel{
				Name:    types.StringValue(v.Name),
				Value:   stringOrNull(v.Value),
				Secure:  types.BoolValue(v.Secure),
				Example: stringOrNull(v.Example),
			})
		}
	}
}

// isEmptyJSONObject reports whether raw is `{}` once whitespace is ignored.
func isEmptyJSONObject(raw []byte) bool {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	return len(obj) == 0
}

// syntheticErrorDetail explains the 404 a server returns when synthetics is
// switched off, which otherwise reads as a missing check.
func syntheticErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if isSyntheticsDisabled(err) {
		msg += "\n\nSynthetics look disabled on this server. The routes are only registered when " +
			"ZO_SYNTHETICS_ENABLED is true, so every synthetics path answers 404 when it is off. Enable it " +
			"on the server, or remove the openobserve_synthetic resources."
	}
	if strings.Contains(msg, "location") {
		msg += "\n\nCheck the location names against the openobserve_synthetic_locations data source: a " +
			"location this deployment does not have is rejected."
	}
	return msg
}
