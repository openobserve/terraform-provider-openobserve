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
	_ resource.Resource                   = &PipelineDestinationResource{}
	_ resource.ResourceWithConfigure      = &PipelineDestinationResource{}
	_ resource.ResourceWithImportState    = &PipelineDestinationResource{}
	_ resource.ResourceWithValidateConfig = &PipelineDestinationResource{}
	_ resource.ResourceWithModifyPlan     = &PipelineDestinationResource{}
)

// Output formats the server accepts for a destination. The first three are
// plain strings on the wire; esbulk and stringseparated are objects carrying
// their one setting, for example {"esbulk":{"index":"logs"}}.
const (
	outputFormatJSON            = "json"
	outputFormatNDJSON          = "ndjson"
	outputFormatNestedEvent     = "nestedevent"
	outputFormatESBulk          = "esbulk"
	outputFormatStringSeparated = "stringseparated"
)

// Values of the computed module attribute.
const (
	destinationModulePipeline = "pipeline"
	destinationModuleAlert    = "alert"
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

	OutputFormat          types.String `tfsdk:"output_format"`
	OutputFormatIndex     types.String `tfsdk:"output_format_index"`
	OutputFormatSeparator types.String `tfsdk:"output_format_separator"`
	Module                types.String `tfsdk:"module"`
}

func (r *PipelineDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pipeline_destination"
}

func (r *PipelineDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve pipeline destination: an external endpoint a pipeline forwards " +
			"records to through a `remote_stream` node.\n\n" +
			"This is the same underlying object as `openobserve_alert_destination`, stored through the same " +
			"endpoints, and the server tells them apart by the `module` query parameter sent on create and " +
			"update, which this resource always sets to `pipeline`. Without it the server stores an alert " +
			"destination, and a pipeline cannot send to it. " +
			"They are separate resources here because that choice decides what the object is for, and because " +
			"the rest of the alert surface, templates, email recipients and SNS, does not apply to a pipeline.\n\n" +
			"Provider versions up to 1.4.2 did not send `module`, so a destination they created is an alert " +
			"destination. The computed `module` attribute reads `alert` for one, and the next plan shows an " +
			"in-place update that converts it. An alert that also sends to that destination stops working " +
			"once it is converted, so point such alerts at an `openobserve_alert_destination` first.\n\n" +
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
			"output_format": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(outputFormatJSON),
				Validators: []validator.String{stringvalidator.OneOf(
					outputFormatJSON, outputFormatNDJSON, outputFormatNestedEvent,
					outputFormatESBulk, outputFormatStringSeparated,
				)},
				Description: "How records are encoded in each request: `json` (one JSON array), `ndjson` " +
					"(one JSON object per line), `nestedevent` (one `{\"event\": ...}` object per line), `esbulk` " +
					"(Elasticsearch bulk format, into the index named by `output_format_index`), or `stringseparated` " +
					"(JSON objects joined by `output_format_separator`). Defaults to `json`, which is also what " +
					"the server uses for a destination saved without one. Leaving it unset means `json`, not " +
					"\"whatever the server has\": a destination whose format was set outside Terraform, for " +
					"example `ndjson` in the UI, plans a change back to `json` on the first refresh after " +
					"upgrading or on import. Set `output_format` to keep the format it has.",
			},
			// Neither setting rejects the empty string. The server stores it without
			// complaint, so a destination saved with one elsewhere has to be
			// describable here, or no configuration could ever match it.
			"output_format_index": schema.StringAttribute{
				Optional: true,
				Description: "Elasticsearch index the records are written to. Required when `output_format` is " +
					"`esbulk`, and not allowed otherwise. OpenObserve also accepts an empty index, which sends " +
					"each record with `\"_index\": \"\"`.",
			},
			"output_format_separator": schema.StringAttribute{
				Optional: true,
				Description: "String placed between records. Required when `output_format` is `stringseparated`, " +
					"and not allowed otherwise. An empty separator joins the records with nothing between them.",
			},
			"module": schema.StringAttribute{
				Computed: true,
				Description: "What the server stores this destination as: `pipeline`, or `alert` for one created by " +
					"provider version 1.4.2 or earlier, which a pipeline cannot send to. When it reads `alert`, the " +
					"plan shows an in-place update that converts the destination to a pipeline destination. " +
					"The check runs only while state has no module recorded, which is after upgrading and on " +
					"import, so it does not cost a listing of every destination on each refresh. Once state " +
					"records `pipeline`, a destination re-saved as an alert destination outside Terraform is not " +
					"detected again.",
				PlanModifiers: []planmodifier.String{destinationModuleAlwaysPipeline{}},
			},
		},
	}
}

// ValidateConfig requires the setting esbulk and stringseparated carry, and
// rejects it for the other formats, which have nowhere to put it.
func (r *PipelineDestinationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checkOutputFormatSettings(&config, &resp.Diagnostics)
}

// ModifyPlan repeats the output_format checks once every value is known.
// ValidateConfig has to skip a value that is unknown until apply, and if it
// then turns out wrong, the server drops the setting and the apply ends in
// "Provider produced inconsistent result" instead of a clear message.
// Terraform plans again with the values known before it applies, which is
// where this catches it.
func (r *PipelineDestinationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan PipelineDestinationResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checkOutputFormatSettings(&plan, &resp.Diagnostics)
}

// checkOutputFormatSettings requires the setting esbulk and stringseparated
// carry, and rejects it with any other format. Anything still unknown is left
// for a later check.
func checkOutputFormatSettings(model *PipelineDestinationResourceModel, diags *diag.Diagnostics) {
	if model.OutputFormat.IsUnknown() {
		return
	}
	format := model.OutputFormat.ValueString()
	if model.OutputFormat.IsNull() {
		format = outputFormatJSON
	}
	checkOutputFormatSetting(diags, model.OutputFormatIndex, "output_format_index", outputFormatESBulk, format)
	checkOutputFormatSetting(diags, model.OutputFormatSeparator, "output_format_separator", outputFormatStringSeparated, format)
}

func checkOutputFormatSetting(diags *diag.Diagnostics, v types.String, attr, owner, format string) {
	if v.IsUnknown() {
		return
	}
	switch {
	case format == owner && v.IsNull():
		diags.AddAttributeError(path.Root(attr), "Missing "+attr,
			fmt.Sprintf("`%s` is required when `output_format` is `%s`.", attr, owner))
	case format != owner && !v.IsNull():
		diags.AddAttributeError(path.Root(attr), "Unexpected "+attr,
			fmt.Sprintf("`%s` only applies when `output_format` is `%s`.", attr, owner))
	}
}

// destinationModuleAlwaysPipeline plans module as "pipeline". Create and
// update send module=pipeline, so that is what the destination is after apply, and
// a state that reads "alert" becomes a planned update that converts it.
type destinationModuleAlwaysPipeline struct{}

func (m destinationModuleAlwaysPipeline) Description(_ context.Context) string {
	return "Always \"pipeline\": create and update store the destination as a pipeline destination."
}

func (m destinationModuleAlwaysPipeline) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m destinationModuleAlwaysPipeline) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	resp.PlanValue = types.StringValue(destinationModulePipeline)
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
		// Never a template: a pipeline destination has none. The client marks it
		// as a pipeline destination with the module=pipeline query parameter.
		DestinationTypeName: optString(model.DestinationType),
		Metadata:            stringsFromMap(ctx, model.Metadata, diags),
		OutputFormat:        outputFormatToAPI(model, diags),
		Emails:              []string{},
	}
}

// outputFormatToAPI encodes the output_format attributes the way the server's
// externally tagged enum expects them.
func outputFormatToAPI(model *PipelineDestinationResourceModel, diags *diag.Diagnostics) json.RawMessage {
	if model.OutputFormat.IsNull() || model.OutputFormat.IsUnknown() {
		return nil
	}
	var v any
	switch format := model.OutputFormat.ValueString(); format {
	case outputFormatESBulk:
		v = map[string]map[string]string{format: {"index": model.OutputFormatIndex.ValueString()}}
	case outputFormatStringSeparated:
		v = map[string]map[string]string{format: {"separator": model.OutputFormatSeparator.ValueString()}}
	default:
		v = format
	}
	raw, err := json.Marshal(v)
	if err != nil {
		diags.AddError("Error encoding output_format", err.Error())
		return nil
	}
	return raw
}

// outputFormatFromAPI decodes the server's output_format into the three
// attributes. A destination saved without one reads back with none, and the
// server sends that destination's records as json, so json is what it
// reports: a destination created before this attribute existed plans clean
// once it is refreshed.
func outputFormatFromAPI(raw json.RawMessage, model *PipelineDestinationResourceModel, diags *diag.Diagnostics) {
	model.OutputFormat = types.StringValue(outputFormatJSON)
	model.OutputFormatIndex = types.StringNull()
	model.OutputFormatSeparator = types.StringNull()
	if len(raw) == 0 || string(raw) == "null" {
		return
	}

	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		model.OutputFormat = types.StringValue(name)
		return
	}
	var tagged map[string]struct {
		Index     *string `json:"index"`
		Separator *string `json:"separator"`
	}
	if err := json.Unmarshal(raw, &tagged); err != nil || len(tagged) != 1 {
		diags.AddError("Unexpected output_format from server",
			fmt.Sprintf("Could not read output_format %s.", string(raw)))
		return
	}
	for name, settings := range tagged {
		model.OutputFormat = types.StringValue(name)
		model.OutputFormatIndex = stringFromPtr(settings.Index)
		model.OutputFormatSeparator = stringFromPtr(settings.Separator)
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
	state.Module = r.readModule(ctx, org, state.Name.ValueString(), state.Module, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readModule reports whether the server stores the destination as a pipeline
// destination or as an alert one. Provider versions up to 1.4.2 created alert
// destinations, and the single-destination GET does not reliably say which it
// is.
//
// The listing needs permission to list destinations, which a role allowed to
// read this one may lack. Failing the refresh over it would make the resource
// unusable for that role, so a failed listing is a warning and module keeps
// the value it had.
//
// Only a destination with no recorded module, which is one written by an
// earlier provider version or one being imported, can read as alert. Every
// write by this version stores a pipeline destination, and the listing can
// leave out one that exists: an enterprise listing is filtered by the
// caller's permissions, and a node's cache can lag behind a write. Trusting
// its absence there would plan the same conversion on every refresh.
func (r *PipelineDestinationResource) readModule(ctx context.Context, org, name string, prior types.String, diags *diag.Diagnostics) types.String {
	if prior.ValueString() == destinationModulePipeline {
		return prior
	}
	names, err := r.client.ListPipelineDestinationNames(ctx, org)
	if err != nil {
		diags.AddWarning("Could not check how the pipeline destination is stored",
			fmt.Sprintf("Listing the pipeline destinations of org %q failed, so the provider cannot tell whether "+
				"%q is still an alert destination left by provider version 1.4.2 or earlier: %s\n\n"+
				"Until it is known, the plan shows module changing to \"pipeline\". Applying once settles it: "+
				"the update stores the destination as a pipeline destination, whichever it was, and this "+
				"check does not run again.", org, name, err))
		return prior
	}
	if names[name] {
		return types.StringValue(destinationModulePipeline)
	}
	return types.StringValue(destinationModuleAlert)
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
	state.Module = r.readModule(ctx, parts[0], parts[1], types.StringNull(), &resp.Diagnostics)
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
	// The write that preceded this read sent module=pipeline.
	model.Module = types.StringValue(destinationModulePipeline)
}

func (r *PipelineDestinationResource) applyToModel(ctx context.Context, api *PipelineDestinationAPI, model *PipelineDestinationResourceModel, diags *diag.Diagnostics) {
	model.Name = types.StringValue(api.Name)
	model.URL = types.StringValue(api.URL)
	model.SkipTLSVerify = types.BoolValue(api.SkipTLSVerify)
	if api.Method != "" {
		model.Method = types.StringValue(api.Method)
	}
	model.DestinationType = stringFromPtr(api.DestinationTypeName)
	outputFormatFromAPI(api.OutputFormat, model, diags)

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
