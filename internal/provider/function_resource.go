package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &FunctionResource{}
	_ resource.ResourceWithConfigure   = &FunctionResource{}
	_ resource.ResourceWithImportState = &FunctionResource{}
)

// NewFunctionResource returns a factory for the openobserve_function resource.
func NewFunctionResource() resource.Resource { return &FunctionResource{} }

// FunctionResource manages a VRL or JavaScript transform function.
type FunctionResource struct{ client *Client }

// FunctionResourceModel holds the Terraform state for a function.
type FunctionResourceModel struct {
	ID       types.String `tfsdk:"id"`
	OrgID    types.String `tfsdk:"org_id"`
	Name     types.String `tfsdk:"name"`
	Function types.String `tfsdk:"function"`
	Params   types.String `tfsdk:"params"`
	Language types.String `tfsdk:"language"`
	NumArgs  types.Int64  `tfsdk:"num_args"`
}

func (r *FunctionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_function"
}

func (r *FunctionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve transform function.\n\n" +
			"A function transforms records as they pass through a pipeline, or enriches them at query time. " +
			"Functions are written in [VRL](https://vrl.dev) by default, and the server compiles the body when " +
			"it is saved, so a syntax error is reported by `terraform apply` rather than discovered later in a " +
			"pipeline that quietly stops working.\n\n" +
			"Reference this resource's `name` from an `openobserve_pipeline` function node. That is what tells " +
			"Terraform to create the function first, and the server refuses to delete a function a pipeline " +
			"still uses.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the function belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Function name, unique within the organization. Referenced by pipeline function nodes.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"function": schema.StringAttribute{
				Required: true,
				Description: "The function body. Use a heredoc for anything beyond one line.\n\n" +
					"For VRL the server appends a trailing `.` when the body does not already end in one, because " +
					"a VRL program's value is its last expression and a transform has to return the record. The " +
					"provider treats that addition as equivalent to what you wrote, so it does not show as drift.",
			},
			"params": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("row"),
				Description: "Comma-separated parameter names the body receives. Defaults to `row`, which is the " +
					"whole record and is what a pipeline transform wants.",
			},
			"language": schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("vrl"),
				Validators: []validator.String{stringvalidator.OneOf("vrl", "js")},
				Description: "`vrl` (default) or `js`. The server compiles the body in this language when saving, " +
					"so choosing the wrong one surfaces as a compile error rather than a runtime surprise.",
			},
			"num_args": schema.Int64Attribute{
				Computed:    true,
				Description: "Number of parameters the server counted in `params`.",
			},
		},
	}
}

func (r *FunctionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *FunctionResource) apiFromModel(model *FunctionResourceModel) FunctionAPI {
	transType := functionTransTypes[model.Language.ValueString()]
	return FunctionAPI{
		Name:      model.Name.ValueString(),
		Function:  model.Function.ValueString(),
		Params:    model.Params.ValueString(),
		TransType: &transType,
	}
}

func (r *FunctionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FunctionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	body := r.apiFromModel(&plan)
	err := r.client.CreateFunction(ctx, org, body)
	if isFunctionAlreadyExists(err) {
		// Adopt a function that already exists rather than failing, matching how
		// streams and organization members behave. The create endpoint refuses
		// to overwrite, so the update endpoint does the work.
		err = r.client.UpdateFunction(ctx, org, plan.Name.ValueString(), body)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating function", pipelineErrorDetail(err))
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

func (r *FunctionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FunctionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	fn, err := r.client.GetFunction(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading function", err.Error())
		return
	}
	if fn == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(functionResourceID(org, state.Name.ValueString()))
	r.applyToModel(fn, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FunctionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FunctionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateFunction(ctx, org, plan.Name.ValueString(), r.apiFromModel(&plan)); err != nil {
		resp.Diagnostics.AddError("Error updating function", pipelineErrorDetail(err))
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

func (r *FunctionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FunctionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteFunction(ctx, org, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error deleting function", pipelineErrorDetail(err))
	}
}

// ImportState supports: terraform import openobserve_function.example default/redact_email
func (r *FunctionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/redact_email`.",
		)
		return
	}
	fn, err := r.client.GetFunction(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading function during import", err.Error())
		return
	}
	if fn == nil {
		resp.Diagnostics.AddError("Function not found", fmt.Sprintf("Function %q not found in org %q.", parts[1], parts[0]))
		return
	}
	state := FunctionResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(parts[1]),
	}
	r.applyToModel(fn, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *FunctionResource) refresh(ctx context.Context, org, name string, model *FunctionResourceModel, diags *diag.Diagnostics) {
	fn, err := r.client.GetFunction(ctx, org, name)
	if err != nil {
		diags.AddError("Error reading function after write", err.Error())
		return
	}
	if fn == nil {
		diags.AddError("Function not found after write",
			fmt.Sprintf("Function %q was not found in org %q after being written.", name, org))
		return
	}
	r.applyToModel(fn, model)
}

func (r *FunctionResource) applyToModel(api *FunctionAPI, model *FunctionResourceModel) {
	model.Name = types.StringValue(api.Name)
	model.Params = types.StringValue(api.Params)
	model.Language = types.StringValue(functionLanguage(api.TransType))
	model.NumArgs = types.Int64Value(api.NumArgs)

	// A VRL program's value is its last expression, so the server appends a
	// trailing `.` when the body does not end in one, to make the transform
	// return the record. Keeping the configured spelling avoids reporting that
	// as a change on every plan.
	if !vrlBodiesEquivalent(model.Function.ValueString(), api.Function) {
		model.Function = types.StringValue(api.Function)
	}
}

// vrlBodiesEquivalent reports whether a stored function body differs from the
// configured one only by the trailing `.` the server adds.
func vrlBodiesEquivalent(configured, stored string) bool {
	return normalizeVRLBody(configured) == normalizeVRLBody(stored)
}

// normalizeVRLBody strips the trailing return expression the server appends,
// along with surrounding whitespace, so two spellings of the same program
// compare equal.
func normalizeVRLBody(body string) string {
	trimmed := strings.TrimSpace(body)
	trimmed = strings.TrimSuffix(trimmed, ".")
	return strings.TrimSpace(trimmed)
}

func functionResourceID(orgID, name string) string { return orgID + "/" + name }

// pipelineErrorDetail annotates the dependency errors the pipeline family
// returns, which name the blocker but not what to do about it.
func pipelineErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	hint := ""
	switch {
	case strings.Contains(msg, "pipeline dependencies"):
		hint = "\n\nA pipeline still uses this function. Destroy the pipeline first, or remove the function " +
			"node from it. Terraform gets that ordering right on its own when the pipeline's function node " +
			"references `openobserve_function.<name>.name` rather than a literal string."
	case strings.Contains(msg, "currently used by pipeline"):
		hint = "\n\nA pipeline still sends to this destination. Destroy the pipeline first, or remove its " +
			"remote_stream node. Referencing `openobserve_pipeline_destination.<name>.name` from that node " +
			"makes Terraform order the two correctly."
	case strings.Contains(msg, "realtime pipeline with same source stream already exists"):
		hint = "\n\nA stream can be the source of only one realtime pipeline. Extend the existing pipeline " +
			"instead, or use a different source stream."
	case strings.Contains(msg, "Function already exist"):
		hint = "\n\nThe create endpoint refuses to overwrite. Import the existing function to manage it:\n" +
			"  terraform import openobserve_function.<name> {org_id}/{function_name}"
	case strings.Contains(msg, "there must be more than 1 node"):
		hint = "\n\nA pipeline needs at least two nodes and one edge connecting them: a source and somewhere " +
			"for records to go."
	case strings.Contains(msg, "source node not found") || strings.Contains(msg, "target node not found"):
		hint = "\n\nAn edge names a node id that no node block declares. Edge endpoints are node ids, not names."
	}
	return msg + hint
}
