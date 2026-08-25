package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &PipelineDestinationResource{}
	_ resource.ResourceWithConfigure   = &PipelineDestinationResource{}
	_ resource.ResourceWithImportState = &PipelineDestinationResource{}
)

// NewPipelineDestinationResource returns a factory for the
// openobserve_pipeline_destination resource.
func NewPipelineDestinationResource() resource.Resource { return &PipelineDestinationResource{} }

// PipelineDestinationResource manages an external endpoint a pipeline ships to.
type PipelineDestinationResource struct{ client *Client }

// PipelineDestinationResourceModel holds the Terraform state.
type PipelineDestinationResourceModel struct {
	ID              types.String `tfsdk:"id"`
	OrgID           types.String `tfsdk:"org_id"`
	Name            types.String `tfsdk:"name"`
	URL             types.String `tfsdk:"url"`
	Method          types.String `tfsdk:"method"`
	SkipTLSVerify   types.Bool   `tfsdk:"skip_tls_verify"`
	Headers         types.Map    `tfsdk:"headers"`
	DestinationType types.String `tfsdk:"destination_type"`
	Metadata        types.Map    `tfsdk:"metadata"`
}

func (r *PipelineDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_destination"
}

func (r *PipelineDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve pipeline destination: an external endpoint a pipeline forwards " +
			"records to through a `remote_stream` node.\n\n" +
			"This is the same underlying object as `openobserve_alert_destination`, stored through the same " +
			"endpoints, and the server tells them apart by one field: a destination carrying a template is an " +
			"alert destination, and one without a template is a pipeline destination that alerts cannot use. " +
			"They are separate resources here because that field decides what the object is for, and because " +
			"the rest of the alert surface, templates, email recipients and SNS, does not apply to a pipeline.\n\n" +
			"The server refuses to delete a destination a pipeline still sends to. Referencing this resource's " +
			"`name` from the pipeline's `remote_stream` node is what makes Terraform order the two correctly.",
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
				Description:   "Destination name, unique within the organization. Referenced by pipeline `remote_stream` nodes.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"url": schema.StringAttribute{
				Required:    true,
				Description: "Endpoint records are forwarded to.",
			},
			"method": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("post"),
				Validators:  []validator.String{stringvalidator.OneOf("post", "put", "get")},
				Description: "HTTP method used to forward records.",
			},
			"skip_tls_verify": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Skip TLS certificate verification. Leave false outside development.",
			},
			"headers": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Extra HTTP headers sent with each request, for example an authorization header.",
			},
			"destination_type": schema.StringAttribute{
				Optional: true,
				Description: "What is on the other end, when it is a system OpenObserve knows how to format for: " +
					"`openobserve`, `splunk`, `elasticsearch`, or `custom`. Leave unset for a plain webhook.",
			},
			"metadata": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Free-form key/value pairs carried alongside the destination.",
			},
		},
	}
}

func (r *PipelineDestinationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *PipelineDestinationResource) apiFromModel(ctx context.Context, model *PipelineDestinationResourceModel, diags *diag.Diagnostics) PipelineDestinationAPI {
	return PipelineDestinationAPI{
		Name:          model.Name.ValueString(),
		URL:           model.URL.ValueString(),
		Method:        model.Method.ValueString(),
		SkipTLSVerify: model.SkipTLSVerify.ValueBool(),
		Headers:       stringsFromMap(ctx, model.Headers, diags),
		// Deliberately never a template: that field is what would turn this into
		// an alert destination.
		DestinationTypeName: optString(model.DestinationType),
		Metadata:            stringsFromMap(ctx, model.Metadata, diags),
		Emails:              []string{},
	}
}

func (r *PipelineDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.apiFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.CreatePipelineDestination(ctx, org, body); err != nil {
		resp.Diagnostics.AddError("Error creating pipeline destination", pipelineErrorDetail(err))
		return
	}
	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(functionResourceID(org, plan.Name.ValueString()))
	r.refresh(ctx, org, plan.Name.ValueString(), &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	dest, err := r.client.GetPipelineDestination(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading pipeline destination", err.Error())
		return
	}
	if dest == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(functionResourceID(org, state.Name.ValueString()))
	r.applyToModel(ctx, dest, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PipelineDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	body := r.apiFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.UpdatePipelineDestination(ctx, org, plan.Name.ValueString(), body); err != nil {
		resp.Diagnostics.AddError("Error updating pipeline destination", pipelineErrorDetail(err))
		return
	}
	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(functionResourceID(org, plan.Name.ValueString()))
	r.refresh(ctx, org, plan.Name.ValueString(), &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PipelineDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeletePipelineDestination(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting pipeline destination", pipelineErrorDetail(err))
	}
}

// ImportState supports: terraform import openobserve_pipeline_destination.example default/ship_to_s3
func (r *PipelineDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/ship_to_s3`.",
		)
		return
	}
	dest, err := r.client.GetPipelineDestination(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading pipeline destination during import", err.Error())
		return
	}
	if dest == nil {
		resp.Diagnostics.AddError("Pipeline destination not found",
			fmt.Sprintf("Destination %q not found in org %q.", parts[1], parts[0]))
		return
	}
	state := PipelineDestinationResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(parts[1]),
	}
	r.applyToModel(ctx, dest, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *PipelineDestinationResource) refresh(ctx context.Context, org, name string, model *PipelineDestinationResourceModel, diags *diag.Diagnostics) {
	dest, err := r.client.GetPipelineDestination(ctx, org, name)
	if err != nil {
		diags.AddError("Error reading pipeline destination after write", err.Error())
		return
	}
	if dest == nil {
		diags.AddError("Pipeline destination not found after write",
			fmt.Sprintf("Destination %q was not found in org %q after being written.", name, org))
		return
	}
	r.applyToModel(ctx, dest, model, diags)
}

func (r *PipelineDestinationResource) applyToModel(ctx context.Context, api *PipelineDestinationAPI, model *PipelineDestinationResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(api.Name)
	model.URL = types.StringValue(api.URL)
	model.SkipTLSVerify = types.BoolValue(api.SkipTLSVerify)
	if api.Method != "" {
		model.Method = types.StringValue(api.Method)
	}
	model.DestinationType = stringFromPtr(api.DestinationTypeName)

	// The server echoes an empty map for both of these when nothing was set.
	// Reporting that as an empty map where the configuration said nothing would
	// be drift, so absent stays null.
	if len(api.Headers) == 0 {
		model.Headers = types.MapNull(types.StringType)
	} else {
		model.Headers = mapFromStrings(ctx, api.Headers, diags)
	}
	if len(api.Metadata) == 0 {
		model.Metadata = types.MapNull(types.StringType)
	} else {
		model.Metadata = mapFromStrings(ctx, api.Metadata, diags)
	}
}
