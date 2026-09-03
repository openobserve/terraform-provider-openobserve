package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &IngestionTokenResource{}
	_ resource.ResourceWithConfigure   = &IngestionTokenResource{}
	_ resource.ResourceWithImportState = &IngestionTokenResource{}
)

// NewIngestionTokenResource returns a factory for the
// openobserve_ingestion_token resource.
func NewIngestionTokenResource() resource.Resource { return &IngestionTokenResource{} }

// IngestionTokenResource manages a token used to send data into an organization.
type IngestionTokenResource struct{ client *Client }

// IngestionTokenResourceModel holds the Terraform state for an ingestion token.
type IngestionTokenResourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	Token       types.String `tfsdk:"token"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	CreatedBy   types.String `tfsdk:"created_by"`
	CreatedAt   types.Int64  `tfsdk:"created_at"`
}

func (r *IngestionTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ingestion_token"
}

func (r *IngestionTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an OpenObserve ingestion token.\n\n" +
			"An ingestion token authenticates data going *into* an organization. Collectors, agents and SDKs " +
			"carry one. This is a different thing from an `openobserve_service_account`, which authenticates " +
			"calls to the management API: use a token to ship data, and a service account to configure.\n\n" +
			"Issuing a token per sender rather than sharing one is what makes it possible to cut off a single " +
			"collector later without re-deploying every other one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource ID in the format `{org_id}/{name}`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"org_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Organization the token belongs to. Defaults to the provider's `org_id`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Token name, unique within the organization. Name it after the sender, since that " +
					"is what you will be looking for when deciding what to disable.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString(""),
				Description:   "What this token is for.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Whether the token is accepted. Setting this to `false` stops ingestion using it " +
					"without destroying anything, which is the safe way to retire a sender.",
			},
			"token": schema.StringAttribute{
				Computed:  true,
				Sensitive: true,
				Description: "The token value. Configure your collector with this.\n\n" +
					"It is stored in Terraform state, so the state backend should be encrypted and access " +
					"controlled.",
			},
			"is_default": schema.BoolAttribute{
				Computed: true,
				Description: "Whether this is the organization's default ingestion token. The server decides " +
					"this; it is not something you set.",
			},
			"created_by": schema.StringAttribute{
				Computed:    true,
				Description: "Who created the token.",
			},
			"created_at": schema.Int64Attribute{
				Computed:    true,
				Description: "When the token was created, in microseconds.",
			},
		},
	}
}

func (r *IngestionTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureResourceClient(req, resp)
}

func (r *IngestionTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan IngestionTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateIngestionToken(ctx, org, CreateIngestionTokenAPI{
		Name:        plan.Name.ValueString(),
		Description: optString(plan.Description),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating ingestion token", ingestionTokenErrorDetail(err))
		return
	}

	// The token is created enabled. Disabling is a separate call, so a
	// configuration that asks for a disabled token needs it made and then
	// turned off.
	if !plan.Enabled.ValueBool() {
		if err := r.client.SetIngestionTokenEnabled(ctx, org, plan.Name.ValueString(), false); err != nil {
			resp.Diagnostics.AddError("Error disabling ingestion token after create", ingestionTokenErrorDetail(err))
			return
		}
		created.Enabled = false
	}

	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(functionResourceID(org, plan.Name.ValueString()))
	r.applyToModel(created, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *IngestionTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state IngestionTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	token, err := r.client.GetIngestionToken(ctx, org, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading ingestion token", ingestionTokenErrorDetail(err))
		return
	}
	if token == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.OrgID = types.StringValue(org)
	state.ID = types.StringValue(functionResourceID(org, state.Name.ValueString()))
	r.applyToModel(token, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *IngestionTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state IngestionTokenResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, plan.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// `enabled` is the only mutable field. Name and description force a
	// replacement, because the API has no way to change them.
	if plan.Enabled.ValueBool() != state.Enabled.ValueBool() {
		if err := r.client.SetIngestionTokenEnabled(ctx, org, plan.Name.ValueString(), plan.Enabled.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Error changing ingestion token enabled state", ingestionTokenErrorDetail(err))
			return
		}
	}

	token, err := r.client.GetIngestionToken(ctx, org, plan.Name.ValueString())
	if err != nil || token == nil {
		resp.Diagnostics.AddError("Error reading ingestion token after update",
			"The token was updated but could not be read back.")
		return
	}
	plan.OrgID = types.StringValue(org)
	plan.ID = types.StringValue(functionResourceID(org, plan.Name.ValueString()))
	r.applyToModel(token, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete disables the token, because OpenObserve exposes no way to remove one.
//
// Reporting success while silently leaving the token usable would be the worse
// option: a token that still authenticates ingestion is a live credential.
// Disabling it revokes it, which is the outcome a destroy is actually asking
// for, and the warning says the record remains.
func (r *IngestionTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state IngestionTokenResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	org := requireOrg(r.client, state.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := state.Name.ValueString()

	token, err := r.client.GetIngestionToken(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading ingestion token before delete", ingestionTokenErrorDetail(err))
		return
	}
	if token == nil {
		// Already gone from the listing, so there is nothing to revoke.
		return
	}

	if token.Enabled {
		if err := r.client.SetIngestionTokenEnabled(ctx, org, name, false); err != nil {
			resp.Diagnostics.AddError("Error disabling ingestion token", ingestionTokenErrorDetail(err))
			return
		}
	}

	resp.Diagnostics.AddWarning(
		"Ingestion token disabled rather than deleted",
		fmt.Sprintf("OpenObserve has no API for deleting an ingestion token, so %q has been disabled and no "+
			"longer authenticates ingestion. The record remains visible in the organization, and Terraform "+
			"no longer tracks it. Re-creating a token with the same name will fail while that record exists.",
			name),
	)
}

// ImportState supports: terraform import openobserve_ingestion_token.example default/otel-collector
func (r *IngestionTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			"Import ID must be `{org_id}/{name}`, for example `default/otel-collector`.",
		)
		return
	}
	token, err := r.client.GetIngestionToken(ctx, parts[0], parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Error reading ingestion token during import", ingestionTokenErrorDetail(err))
		return
	}
	if token == nil {
		resp.Diagnostics.AddError("Ingestion token not found",
			fmt.Sprintf("Ingestion token %q not found in org %q.", parts[1], parts[0]))
		return
	}
	state := IngestionTokenResourceModel{
		ID:    types.StringValue(req.ID),
		OrgID: types.StringValue(parts[0]),
		Name:  types.StringValue(parts[1]),
	}
	r.applyToModel(token, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *IngestionTokenResource) applyToModel(api *IngestionTokenAPI, model *IngestionTokenResourceModel) {
	model.Name = types.StringValue(api.Name)
	model.Description = types.StringValue(api.Description)
	model.Enabled = types.BoolValue(api.Enabled)
	model.IsDefault = types.BoolValue(api.IsDefault)
	model.CreatedBy = types.StringValue(api.CreatedBy)
	model.CreatedAt = types.Int64Value(api.CreatedAt)

	// The listing reports the token, so a read keeps it current. An import of a
	// token whose secret the server withholds leaves whatever state already
	// held rather than blanking it.
	if api.Token != "" {
		model.Token = types.StringValue(api.Token)
	}
}

// ingestionTokenErrorDetail annotates the permission error this API returns,
// which names a role requirement without saying whose role it means.
func ingestionTokenErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "role required to") {
		msg += "\n\nIngestion tokens can only be managed by a user with the Admin or Root role in the " +
			"organization. Check the role of the account the provider authenticates as, not the role of " +
			"whoever will use the token."
	}
	return msg
}
