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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &AlertDestinationResource{}
	_ resource.ResourceWithConfigure      = &AlertDestinationResource{}
	_ resource.ResourceWithImportState    = &AlertDestinationResource{}
	_ resource.ResourceWithValidateConfig = &AlertDestinationResource{}
)

// NewAlertDestinationResource returns a factory for the openobserve_alert_destination resource.
func NewAlertDestinationResource() resource.Resource {
	return &AlertDestinationResource{}
}

// AlertDestinationResource manages where alert notifications are delivered.
type AlertDestinationResource struct {
	client *Client
}

// AlertDestinationResourceModel holds the Terraform state for a destination.
type AlertDestinationResourceModel struct {
	ID              types.String `tfsdk:"id"`
	OrgID           types.String `tfsdk:"org_id"`
	Name            types.String `tfsdk:"name"`
	DestinationType types.String `tfsdk:"type"`
	Template        types.String `tfsdk:"template"`
	URL             types.String `tfsdk:"url"`
	Method          types.String `tfsdk:"method"`
	SkipTLSVerify   types.Bool   `tfsdk:"skip_tls_verify"`
	Headers         types.Map    `tfsdk:"headers"`
	Emails          types.Set    `tfsdk:"emails"`
	SNSTopicARN     types.String `tfsdk:"sns_topic_arn"`
	AWSRegion       types.String `tfsdk:"aws_region"`
	Metadata        types.Map    `tfsdk:"metadata"`
}

func (r *AlertDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_destination"
}

func (r *AlertDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an alert destination: where OpenObserve delivers a notification when an alert fires. " +
			"A destination is a webhook (`http`), a set of email recipients (`email`), or an AWS SNS topic (`sns`).\n\n" +
			"OpenObserve replaces the whole destination on update. Settings this resource does not manage yet are " +
			"read from the server before each update and sent back unchanged, so a prebuilt destination created " +
			"in the UI (PagerDuty, Jira, Splunk and the like) keeps its kind: `output_format` and " +
			"`destination_type_name`. `metadata` is kept the same way while it is unset.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the destination belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Destination name, unique within the organization.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("http"),
				Description: "Delivery channel: `http` (default), `email`, or `sns`.",
				Validators:  []validator.String{stringvalidator.OneOf(destinationTypes...)},
			},
			"template": schema.StringAttribute{
				Optional: true,
				Description: "Name of the `openobserve_alert_template` used to render the message. Required for a " +
					"destination to be usable by alerts; a destination without a template is treated as a pipeline destination.",
			},
			"url": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
				Description: "Webhook URL. Required when `type` is `http`.",
			},
			"method": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("post"),
				Description: "HTTP method for webhook delivery: `post` (default), `put`, or `get`.",
				Validators:  []validator.String{stringvalidator.OneOf("post", "put", "get")},
			},
			"skip_tls_verify": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Skip TLS certificate verification when calling the webhook.",
			},
			"headers": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Additional HTTP headers sent with each webhook request, for example an authorization header.",
			},
			"emails": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Recipient addresses. Required when `type` is `email`. Every address must belong to a user in the organization.",
			},
			"sns_topic_arn": schema.StringAttribute{
				Optional:    true,
				Description: "SNS topic ARN. Required when `type` is `sns`.",
			},
			"aws_region": schema.StringAttribute{
				Optional:    true,
				Description: "AWS region of the SNS topic. Required when `type` is `sns`.",
			},
			"metadata": schema.MapAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				// The server keeps some credentials here, a PagerDuty routing key
				// for one, so the values stay out of plan output like headers do.
				Sensitive: true,
				// Unset, the plan carries the value last read, and the update
				// sends what the server holds; see metadataForUpdate.
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
				Description: "Free-form key/value pairs stored with a webhook (`http`) destination, the same " +
					"map `openobserve_pipeline_destination` carries. The OpenObserve UI records some of its own " +
					"here, such as `prebuilt_type` and `render_format`.\n\n" +
					"Leave it unset to keep whatever the server holds: the provider reads the current map and " +
					"sends it back on every update, so metadata written by the UI or the API is not wiped by an " +
					"unrelated change. Set it to manage the whole map, and keys added elsewhere then show as " +
					"drift. Set it to `{}` to clear it. Removing the attribute from a configuration that set it " +
					"stops managing it and leaves the server's map as it is. The map is read again at apply " +
					"time, so if it changed on the server after the plan was made, the update still sends the " +
					"server's map. State records the planned map for that apply and picks up the server's on " +
					"the next refresh, with no diff.\n\n" +
					"Only webhook destinations store metadata, so a non-empty map is rejected with `type` `email` " +
					"or `sns`.\n\n" +
					"Marked sensitive, unlike on `openobserve_pipeline_destination`, because the server keeps some " +
					"credentials here, a PagerDuty routing key for one. The cost is that a plan shows that the map " +
					"changed but not which key; `nonsensitive()` reveals it where that is acceptable.",
			},
		},
	}
}

// ValidateConfig rejects combinations the API would reject at apply time, so
// the mistake surfaces during plan instead.
func (r *AlertDestinationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config AlertDestinationResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An unknown type at plan time cannot be checked; the API validates it.
	if config.DestinationType.IsUnknown() {
		return
	}
	destType := config.DestinationType.ValueString()
	if destType == "" {
		destType = "http"
	}

	if !config.Metadata.IsNull() && !config.Metadata.IsUnknown() && len(config.Metadata.Elements()) > 0 && destType != "http" {
		resp.Diagnostics.AddAttributeError(
			path.Root("metadata"),
			"Metadata is only stored on webhook destinations",
			fmt.Sprintf("The server keeps `metadata` only for `type` `http` and drops it for `%s`, so it would "+
				"never read back. Remove `metadata`, or use an `http` destination.", destType),
		)
	}

	switch destType {
	case "email":
		if config.Emails.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("emails"),
				"Missing emails",
				"`emails` is required when `type` is `email`.",
			)
		}
	case "sns":
		if config.SNSTopicARN.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("sns_topic_arn"),
				"Missing sns_topic_arn",
				"`sns_topic_arn` is required when `type` is `sns`.",
			)
		}
		if config.AWSRegion.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("aws_region"),
				"Missing aws_region",
				"`aws_region` is required when `type` is `sns`.",
			)
		}
	case "http":
		if config.URL.IsNull() || (!config.URL.IsUnknown() && config.URL.ValueString() == "") {
			resp.Diagnostics.AddAttributeError(
				path.Root("url"),
				"Missing url",
				"`url` is required when `type` is `http`.",
			)
		}
	}
}

func (r *AlertDestinationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *AlertDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AlertDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.destinationFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.CreateAlertDestination(ctx, org, body); err != nil {
		resp.Diagnostics.AddError("Error creating alert destination", err.Error())
		return
	}

	// Unset on create means none was sent, so none is stored.
	if plan.Metadata.IsUnknown() {
		plan.Metadata = types.MapNull(types.StringType)
	}
	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(alertDestinationResourceID(org, plan.Name.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AlertDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AlertDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	dest, err := r.client.GetAlertDestination(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert destination", err.Error())
		return
	}
	if dest == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(alertDestinationResourceID(org, dest.Name))
	r.applyDestinationToModel(ctx, dest, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AlertDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.destinationFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()

	var configuredMetadata types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("metadata"), &configuredMetadata)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.keepUnmanagedFields(ctx, org, name, configuredMetadata, &body, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if configuredMetadata.IsNull() {
		// The plan carries the map last read, and state keeps it, as Terraform
		// requires, even when the server's map changed after the plan was made;
		// the next refresh reads that without a diff, because the attribute is
		// computed and unset. Only state from a release without the attribute
		// plans it unknown, and then state takes what was sent.
		if plan.Metadata.IsUnknown() {
			plan.Metadata = mapFromStrings(ctx, body.Metadata, &resp.Diagnostics)
		}
	}

	if err := r.client.UpdateAlertDestination(ctx, org, name, body); err != nil {
		resp.Diagnostics.AddError("Error updating alert destination", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(alertDestinationResourceID(org, name))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// keepUnmanagedFields fills in what an update sends for the fields the
// configuration does not decide.
//
// The update endpoint replaces the whole destination, so any field the body
// leaves out is cleared. output_format and destination_type_name have no
// attribute, and metadata may be unset; those are read just before the
// write and sent back. State is not used for this: it may be stale or, for
// state written by a release that did not know the attribute, empty. Losing
// destination_type_name would leave a prebuilt destination half converted,
// still marked prebuilt in its metadata but no longer formatted as one.
func (r *AlertDestinationResource) keepUnmanagedFields(ctx context.Context, org, name string, configuredMetadata types.Map, body *AlertDestinationAPI, diags *diag.Diagnostics) {
	current, err := r.client.GetAlertDestination(ctx, org, name)
	if err != nil {
		diags.AddError("Error reading alert destination before update",
			"The update replaces the whole destination, so the provider reads the settings it does not "+
				"manage, to send them back, and that read failed: "+err.Error())
		return
	}
	if current == nil {
		return
	}
	body.OutputFormat = current.OutputFormat
	body.DestinationTypeName = current.DestinationTypeName
	if configuredMetadata.IsNull() {
		body.Metadata = current.Metadata
	}
}

func (r *AlertDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AlertDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAlertDestination(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting alert destination", err.Error())
	}
}

// ImportState supports: terraform import openobserve_alert_destination.example default/pagerduty
func (r *AlertDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/pagerduty`.",
		)
		return
	}

	dest, err := r.client.GetAlertDestination(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert destination during import", err.Error())
		return
	}
	if dest == nil {
		resp.Diagnostics.AddError("Alert destination not found", fmt.Sprintf("Destination %q not found in org %q.", parts[1], parts[0]))
		return
	}

	state := AlertDestinationResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(dest.Name),
	}
	r.applyDestinationToModel(ctx, dest, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *AlertDestinationResource) destinationFromModel(ctx context.Context, model *AlertDestinationResourceModel, diags *diag.Diagnostics) AlertDestinationAPI {
	emails := stringsFromSet(ctx, model.Emails, diags)
	if emails == nil {
		emails = []string{}
	}
	return AlertDestinationAPI{
		Name:            model.Name.ValueString(),
		DestinationType: model.DestinationType.ValueString(),
		Template:        optString(model.Template),
		URL:             model.URL.ValueString(),
		Method:          model.Method.ValueString(),
		SkipTLSVerify:   model.SkipTLSVerify.ValueBool(),
		Headers:         stringsFromMap(ctx, model.Headers, diags),
		Emails:          emails,
		SNSTopicARN:     optString(model.SNSTopicARN),
		AWSRegion:       optString(model.AWSRegion),
		Metadata:        stringsFromMap(ctx, model.Metadata, diags),
	}
}

func (r *AlertDestinationResource) applyDestinationToModel(ctx context.Context, dest *AlertDestinationAPI, model *AlertDestinationResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(dest.Name)
	model.DestinationType = types.StringValue(dest.DestinationType)
	model.Template = stringFromPtr(dest.Template)
	model.URL = types.StringValue(dest.URL)
	model.Method = types.StringValue(dest.Method)
	model.SkipTLSVerify = types.BoolValue(dest.SkipTLSVerify)
	model.Headers = mapFromStrings(ctx, dest.Headers, diags)
	model.SNSTopicARN = stringFromPtr(dest.SNSTopicARN)
	model.AWSRegion = stringFromPtr(dest.AWSRegion)

	// The server reports no metadata as an empty map. A configured `{}` stays
	// `{}`; anything else that is empty reads as null, so an unset attribute
	// shows no drift.
	configuredEmpty := !model.Metadata.IsNull() && !model.Metadata.IsUnknown() && len(model.Metadata.Elements()) == 0
	if len(dest.Metadata) != 0 || !configuredEmpty {
		model.Metadata = mapFromStrings(ctx, dest.Metadata, diags)
	}

	if len(dest.Emails) == 0 {
		model.Emails = types.SetNull(types.StringType)
	} else {
		model.Emails = setFromStrings(ctx, dest.Emails, diags)
	}
}

func alertDestinationResourceID(orgID, name string) string {
	return fmt.Sprintf("%s/%s", orgID, name)
}
