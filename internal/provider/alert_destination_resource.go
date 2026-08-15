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
}

func (r *AlertDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_destination"
}

func (r *AlertDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an alert destination: where OpenObserve delivers a notification when an alert fires. " +
			"A destination is a webhook (`http`), a set of email recipients (`email`), or an AWS SNS topic (`sns`).",
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

	if err := r.client.UpdateAlertDestination(ctx, org, name, body); err != nil {
		resp.Diagnostics.AddError("Error updating alert destination", err.Error())
		return
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(alertDestinationResourceID(org, name))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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

	if len(dest.Emails) == 0 {
		model.Emails = types.SetNull(types.StringType)
	} else {
		model.Emails = setFromStrings(ctx, dest.Emails, diags)
	}
}

func alertDestinationResourceID(orgID, name string) string {
	return fmt.Sprintf("%s/%s", orgID, name)
}
