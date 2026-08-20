package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &CompositeAlertDataSource{}
	_ datasource.DataSourceWithConfigure = &CompositeAlertReferencesDataSource{}
)

// compositeChildAttrTypes describes one resolved child of a composite.
var compositeChildAttrTypes = map[string]attr.Type{
	"alert_id":   types.StringType,
	"accessible": types.BoolType,
	"name":       types.StringType,
	"alert_type": types.StringType,
	"folder_id":  types.StringType,
	"enabled":    types.BoolType,
	"level":      types.StringType,
	"level_at":   types.Int64Type,
	"stale":      types.BoolType,
	"truth":      types.BoolType,
}

// compositeEvaluationAttrTypes describes a composite's own last verdict.
var compositeEvaluationAttrTypes = map[string]attr.Type{
	"result":       types.BoolType,
	"level":        types.StringType,
	"evaluated_at": types.Int64Type,
}

func compositeChildrenValue(children []CompositeChildAPI, diags *diag.Diagnostics) types.List {
	values := make([]attr.Value, 0, len(children))
	for _, child := range children {
		values = append(values, objectValue(compositeChildAttrTypes, map[string]attr.Value{
			"alert_id":   types.StringValue(child.AlertID),
			"accessible": types.BoolValue(child.Accessible),
			"name":       stringFromPtr(child.Name),
			"alert_type": stringFromPtr(child.AlertType),
			"folder_id":  stringFromPtr(child.FolderID),
			"enabled":    boolFromPtr(child.Enabled),
			"level":      stringFromPtr(child.Level),
			"level_at":   int64FromPtr(child.LevelAt),
			"stale":      boolFromPtr(child.Stale),
			"truth":      boolFromPtr(child.Truth),
		}, diags))
	}
	list, d := types.ListValue(types.ObjectType{AttrTypes: compositeChildAttrTypes}, values)
	diags.Append(d...)
	return list
}

// ---------------------------------------------------------------------------
// Composite alert
// ---------------------------------------------------------------------------

// NewCompositeAlertDataSource returns a factory for the openobserve_composite_alert data source.
func NewCompositeAlertDataSource() datasource.DataSource { return &CompositeAlertDataSource{} }

// CompositeAlertDataSource reads a single composite alert by ID or by name.
type CompositeAlertDataSource struct{ client *Client }

// CompositeAlertDataSourceModel holds the state for a composite lookup.
type CompositeAlertDataSourceModel struct {
	ID                    types.String `tfsdk:"id"`
	OrgID                 types.String `tfsdk:"org_id"`
	AlertID               types.String `tfsdk:"alert_id"`
	Name                  types.String `tfsdk:"name"`
	FolderID              types.String `tfsdk:"folder_id"`
	Description           types.String `tfsdk:"description"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	Expression            types.String `tfsdk:"expression"`
	WarningCountsAsFiring types.Bool   `tfsdk:"warning_counts_as_firing"`
	StaleChildPolicy      types.String `tfsdk:"stale_child_policy"`
	Silence               types.Int64  `tfsdk:"silence"`
	Destinations          types.Set    `tfsdk:"destinations"`
	Tags                  types.Set    `tfsdk:"tags"`
	Priority              types.Int64  `tfsdk:"priority"`
	SchedulerJobPresent   types.Bool   `tfsdk:"scheduler_job_present"`
	Children              types.List   `tfsdk:"children"`
	Evaluation            types.Object `tfsdk:"evaluation"`
}

func (d *CompositeAlertDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_composite_alert"
}

func (d *CompositeAlertDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing composite alert, looked up either by `alert_id` or by `name`.\n\n" +
			"Alongside the configuration, this reports what the composite currently sees: each child's severity, " +
			"whether that child's state has gone stale, and how it evaluated. That is the fastest way to answer why a " +
			"composite is or is not firing without opening the UI.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{alert_id}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"alert_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Composite alert identifier. Supply this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Composite alert name. Supply this or `alert_id`.",
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Restrict a name lookup to one folder. All folders are searched when omitted.",
			},
			"description":              schema.StringAttribute{Computed: true, Description: "What the composite watches for."},
			"enabled":                  schema.BoolAttribute{Computed: true, Description: "Whether the composite is evaluated."},
			"expression":               schema.StringAttribute{Computed: true, Description: "The stored boolean expression, in the fully parenthesized form the server canonicalizes to."},
			"warning_counts_as_firing": schema.BoolAttribute{Computed: true, Description: "Whether a child at warning severity counts as true."},
			"stale_child_policy":       schema.StringAttribute{Computed: true, Description: "What a stale child contributes to the expression."},
			"silence":                  schema.Int64Attribute{Computed: true, Description: "Minutes the composite stays quiet after firing."},
			"destinations": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Destinations notified when the composite fires.",
			},
			"tags": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Selection tags on the composite.",
			},
			"priority":              schema.Int64Attribute{Computed: true, Description: "Configured priority, 1 (most urgent) to 5."},
			"scheduler_job_present": schema.BoolAttribute{Computed: true, Description: "Whether a scheduler job currently backs this composite."},
			"children": schema.ListNestedAttribute{
				Computed: true,
				Description: "The children the expression resolves to, in expression order, each with the state the " +
					"composite read from it.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"alert_id": schema.StringAttribute{Computed: true, Description: "Child alert identifier."},
						"accessible": schema.BoolAttribute{
							Computed: true,
							Description: "Whether the caller may read this child. When false every other attribute " +
								"here is null, and the composite still evaluates it.",
						},
						"name":       schema.StringAttribute{Computed: true, Description: "Child alert name."},
						"alert_type": schema.StringAttribute{Computed: true, Description: "Child kind: `scheduled`, `slo`, or `composite`."},
						"folder_id":  schema.StringAttribute{Computed: true, Description: "Folder holding the child."},
						"enabled":    schema.BoolAttribute{Computed: true, Description: "Whether the child is evaluated."},
						"level":      schema.StringAttribute{Computed: true, Description: "Severity of the child's last classification: `ok`, `warning`, or `critical`."},
						"level_at":   schema.Int64Attribute{Computed: true, Description: "When `level` was recorded, in microseconds."},
						"stale": schema.BoolAttribute{
							Computed: true,
							Description: "Whether the child's state is older than three times its own cadence. A stale " +
								"child contributes whatever `stale_child_policy` dictates rather than its last value.",
						},
						"truth": schema.BoolAttribute{Computed: true, Description: "What this child contributed to the expression on the last evaluation."},
					},
				},
			},
			"evaluation": schema.SingleNestedAttribute{
				Computed: true,
				Description: "The composite's own most recent verdict. Null when it has never been evaluated, which " +
					"is a different thing from having evaluated to false.",
				Attributes: map[string]schema.Attribute{
					"result":       schema.BoolAttribute{Computed: true, Description: "Whether the expression evaluated true."},
					"level":        schema.StringAttribute{Computed: true, Description: "Severity of the result."},
					"evaluated_at": schema.Int64Attribute{Computed: true, Description: "When the result was recorded, in microseconds."},
				},
			},
		},
	}
}

func (d *CompositeAlertDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *CompositeAlertDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data CompositeAlertDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	alertID := data.AlertID.ValueString()
	if alertID == "" {
		name := data.Name.ValueString()
		if name == "" {
			resp.Diagnostics.AddError(
				"Missing composite alert selector",
				"Set either `alert_id` or `name` to identify the composite alert.",
			)
			return
		}
		found, err := d.client.FindAlertByName(ctx, org, data.FolderID.ValueString(), name)
		if err != nil {
			resp.Diagnostics.AddError("Error searching for composite alert", err.Error())
			return
		}
		if found == nil {
			resp.Diagnostics.AddError("Composite alert not found", fmt.Sprintf("No alert named %q in org %q.", name, org))
			return
		}
		if found.AlertType != "composite" {
			resp.Diagnostics.AddError(
				"Not a composite alert",
				fmt.Sprintf("Alert %q is a %s alert. Use the openobserve_alert data source for it.", name, found.AlertType),
			)
			return
		}
		alertID = found.AlertID
	}

	composite, err := d.client.GetCompositeAlert(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading composite alert", err.Error())
		return
	}
	if composite == nil {
		resp.Diagnostics.AddError("Composite alert not found", fmt.Sprintf("Composite alert %q not found in org %q.", alertID, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.AlertID = types.StringValue(alertID)
	data.ID = types.StringValue(alertResourceID(org, alertID))
	data.Name = types.StringValue(composite.Name)
	data.FolderID = types.StringValue(composite.FolderID)
	data.Description = stringFromPtr(composite.Description)
	data.Enabled = types.BoolValue(composite.Enabled)
	data.Expression = types.StringValue(composite.CompositeCondition.Expression)
	data.WarningCountsAsFiring = types.BoolValue(composite.CompositeCondition.WarningCountsAsFiring)
	data.StaleChildPolicy = types.StringValue(composite.CompositeCondition.StaleChildPolicy)
	data.Silence = types.Int64Value(composite.TriggerCondition.Silence)
	data.Destinations = setFromStrings(ctx, composite.Destinations, &resp.Diagnostics)
	data.Tags = setFromStrings(ctx, composite.Tags, &resp.Diagnostics)
	data.Priority = int64FromPtr(composite.Priority)
	data.SchedulerJobPresent = boolFromPtr(composite.SchedulerJobPresent)
	data.Children = compositeChildrenValue(composite.Children, &resp.Diagnostics)

	// A composite that has never run is not a composite that evaluated false.
	if composite.Evaluation == nil {
		data.Evaluation = types.ObjectNull(compositeEvaluationAttrTypes)
	} else {
		data.Evaluation = objectValue(compositeEvaluationAttrTypes, map[string]attr.Value{
			"result":       types.BoolValue(composite.Evaluation.Result),
			"level":        types.StringValue(composite.Evaluation.Level),
			"evaluated_at": int64FromPtr(composite.Evaluation.EvaluatedAt),
		}, &resp.Diagnostics)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Composite alert references
// ---------------------------------------------------------------------------

// compositeReferenceAttrTypes describes one composite that references an alert.
var compositeReferenceAttrTypes = map[string]attr.Type{
	"alert_id":  types.StringType,
	"name":      types.StringType,
	"folder_id": types.StringType,
}

// NewCompositeAlertReferencesDataSource returns a factory for the
// openobserve_composite_alert_references data source.
func NewCompositeAlertReferencesDataSource() datasource.DataSource {
	return &CompositeAlertReferencesDataSource{}
}

// CompositeAlertReferencesDataSource lists the composites that use an alert as a child.
type CompositeAlertReferencesDataSource struct{ client *Client }

// CompositeAlertReferencesDataSourceModel holds the state for a reference lookup.
type CompositeAlertReferencesDataSourceModel struct {
	ID                   types.String `tfsdk:"id"`
	OrgID                types.String `tfsdk:"org_id"`
	AlertID              types.String `tfsdk:"alert_id"`
	References           types.List   `tfsdk:"references"`
	HiddenReferenceCount types.Int64  `tfsdk:"hidden_reference_count"`
}

func (d *CompositeAlertReferencesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_composite_alert_references"
}

func (d *CompositeAlertReferencesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the composite alerts that reference a given alert as a child.\n\n" +
			"The server refuses to delete an alert while a composite still names it, so this answers \"why can I not " +
			"destroy this alert\" directly, including for composites that were created outside Terraform.",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{alert_id}`."},
			"org_id":   schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"alert_id": schema.StringAttribute{Required: true, Description: "Alert or composite alert identifier to look up parents for."},
			"references": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Composites that reference this alert and that the caller may read.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"alert_id":  schema.StringAttribute{Computed: true, Description: "Referencing composite's identifier."},
						"name":      schema.StringAttribute{Computed: true, Description: "Referencing composite's name."},
						"folder_id": schema.StringAttribute{Computed: true, Description: "Folder holding the referencing composite."},
					},
				},
			},
			"hidden_reference_count": schema.Int64Attribute{
				Computed: true,
				Description: "How many referencing composites permissions hid from this caller. When this is not zero, " +
					"an empty `references` list does not mean the alert is unreferenced, and a delete may still be refused.",
			},
		},
	}
}

func (d *CompositeAlertReferencesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *CompositeAlertReferencesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data CompositeAlertReferencesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	alertID := data.AlertID.ValueString()

	refs, err := d.client.ListCompositeReferences(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading composite references", err.Error())
		return
	}
	if refs == nil {
		refs = &CompositeReferencesAPI{}
	}

	values := make([]attr.Value, 0, len(refs.References))
	for _, ref := range refs.References {
		values = append(values, objectValue(compositeReferenceAttrTypes, map[string]attr.Value{
			"alert_id":  types.StringValue(ref.AlertID),
			"name":      types.StringValue(ref.Name),
			"folder_id": types.StringValue(ref.FolderID),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: compositeReferenceAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(alertResourceID(org, alertID))
	data.References = list
	data.HiddenReferenceCount = types.Int64Value(refs.HiddenReferenceCount)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
