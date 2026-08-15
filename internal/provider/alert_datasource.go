package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &AlertTemplateDataSource{}
	_ datasource.DataSourceWithConfigure = &AlertTemplatesDataSource{}
	_ datasource.DataSourceWithConfigure = &AlertDestinationDataSource{}
	_ datasource.DataSourceWithConfigure = &AlertDestinationsDataSource{}
	_ datasource.DataSourceWithConfigure = &AlertDataSource{}
	_ datasource.DataSourceWithConfigure = &AlertsDataSource{}
)

// ---------------------------------------------------------------------------
// Alert templates
// ---------------------------------------------------------------------------

// NewAlertTemplateDataSource returns a factory for the openobserve_alert_template data source.
func NewAlertTemplateDataSource() datasource.DataSource { return &AlertTemplateDataSource{} }

// AlertTemplateDataSource reads a single alert template.
type AlertTemplateDataSource struct{ client *Client }

// AlertTemplateDataSourceModel holds the state for a template lookup.
type AlertTemplateDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	OrgID        types.String `tfsdk:"org_id"`
	Name         types.String `tfsdk:"name"`
	Body         types.String `tfsdk:"body"`
	TemplateType types.String `tfsdk:"type"`
	Title        types.String `tfsdk:"title"`
	IsDefault    types.Bool   `tfsdk:"is_default"`
	IsPrebuilt   types.Bool   `tfsdk:"is_prebuilt"`
}

func (d *AlertTemplateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_template"
}

func (d *AlertTemplateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing alert template, including the prebuilt templates OpenObserve ships with.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{name}`."},
			"org_id":      schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"name":        schema.StringAttribute{Required: true, Description: "Template name to look up."},
			"body":        schema.StringAttribute{Computed: true, Description: "Message body."},
			"type":        schema.StringAttribute{Computed: true, Description: "Channel the template renders for."},
			"title":       schema.StringAttribute{Computed: true, Description: "Email subject line, for email templates."},
			"is_default":  schema.BoolAttribute{Computed: true, Description: "Whether this is the organization's default template."},
			"is_prebuilt": schema.BoolAttribute{Computed: true, Description: "Whether the template is system-managed and read-only."},
		},
	}
}

func (d *AlertTemplateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertTemplateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertTemplateDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()

	template, err := d.client.GetAlertTemplate(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert template", err.Error())
		return
	}
	if template == nil {
		resp.Diagnostics.AddError("Alert template not found", fmt.Sprintf("Template %q not found in org %q.", name, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(alertTemplateResourceID(org, template.Name))
	data.Body = types.StringValue(template.Body)
	data.TemplateType = types.StringValue(template.TemplateType)
	data.Title = types.StringValue(template.Title)
	data.IsDefault = types.BoolValue(template.IsDefault != nil && *template.IsDefault)
	data.IsPrebuilt = types.BoolValue(template.IsPrebuilt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewAlertTemplatesDataSource returns a factory for the openobserve_alert_templates data source.
func NewAlertTemplatesDataSource() datasource.DataSource { return &AlertTemplatesDataSource{} }

// AlertTemplatesDataSource lists the alert templates of an organization.
type AlertTemplatesDataSource struct{ client *Client }

// AlertTemplatesDataSourceModel holds the state for a template listing.
type AlertTemplatesDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	OrgID     types.String `tfsdk:"org_id"`
	Templates types.List   `tfsdk:"templates"`
}

var alertTemplateAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"body":        types.StringType,
	"type":        types.StringType,
	"title":       types.StringType,
	"is_default":  types.BoolType,
	"is_prebuilt": types.BoolType,
}

func (d *AlertTemplatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_templates"
}

func (d *AlertTemplatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the alert templates of an OpenObserve organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"templates": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The templates found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":        schema.StringAttribute{Computed: true, Description: "Template name."},
						"body":        schema.StringAttribute{Computed: true, Description: "Message body."},
						"type":        schema.StringAttribute{Computed: true, Description: "Channel the template renders for."},
						"title":       schema.StringAttribute{Computed: true, Description: "Email subject line."},
						"is_default":  schema.BoolAttribute{Computed: true, Description: "Whether this is the default template."},
						"is_prebuilt": schema.BoolAttribute{Computed: true, Description: "Whether the template is system-managed."},
					},
				},
			},
		},
	}
}

func (d *AlertTemplatesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertTemplatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertTemplatesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	templates, err := d.client.ListAlertTemplates(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing alert templates", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(templates))
	for _, t := range templates {
		values = append(values, objectValue(alertTemplateAttrTypes, map[string]attr.Value{
			"name":        types.StringValue(t.Name),
			"body":        types.StringValue(t.Body),
			"type":        types.StringValue(t.TemplateType),
			"title":       types.StringValue(t.Title),
			"is_default":  types.BoolValue(t.IsDefault != nil && *t.IsDefault),
			"is_prebuilt": types.BoolValue(t.IsPrebuilt),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: alertTemplateAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/alerts/templates", org))
	data.Templates = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Alert destinations
// ---------------------------------------------------------------------------

// NewAlertDestinationDataSource returns a factory for the openobserve_alert_destination data source.
func NewAlertDestinationDataSource() datasource.DataSource { return &AlertDestinationDataSource{} }

// AlertDestinationDataSource reads a single alert destination.
type AlertDestinationDataSource struct{ client *Client }

// AlertDestinationDataSourceModel holds the state for a destination lookup.
type AlertDestinationDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	OrgID           types.String `tfsdk:"org_id"`
	Name            types.String `tfsdk:"name"`
	DestinationType types.String `tfsdk:"type"`
	Template        types.String `tfsdk:"template"`
	URL             types.String `tfsdk:"url"`
	Method          types.String `tfsdk:"method"`
	SkipTLSVerify   types.Bool   `tfsdk:"skip_tls_verify"`
	Emails          types.Set    `tfsdk:"emails"`
	SNSTopicARN     types.String `tfsdk:"sns_topic_arn"`
	AWSRegion       types.String `tfsdk:"aws_region"`
}

func (d *AlertDestinationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_destination"
}

func (d *AlertDestinationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing alert destination. Headers are not returned, because they can carry credentials.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{name}`."},
			"org_id":          schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"name":            schema.StringAttribute{Required: true, Description: "Destination name to look up."},
			"type":            schema.StringAttribute{Computed: true, Description: "Delivery channel."},
			"template":        schema.StringAttribute{Computed: true, Description: "Template used to render the message."},
			"url":             schema.StringAttribute{Computed: true, Description: "Webhook URL."},
			"method":          schema.StringAttribute{Computed: true, Description: "HTTP method used for webhook delivery."},
			"skip_tls_verify": schema.BoolAttribute{Computed: true, Description: "Whether TLS verification is skipped."},
			"emails": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Recipient addresses for email destinations.",
			},
			"sns_topic_arn": schema.StringAttribute{Computed: true, Description: "SNS topic ARN."},
			"aws_region":    schema.StringAttribute{Computed: true, Description: "AWS region of the SNS topic."},
		},
	}
}

func (d *AlertDestinationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertDestinationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertDestinationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := data.Name.ValueString()

	dest, err := d.client.GetAlertDestination(ctx, org, name)
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert destination", err.Error())
		return
	}
	if dest == nil {
		resp.Diagnostics.AddError("Alert destination not found", fmt.Sprintf("Destination %q not found in org %q.", name, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(alertDestinationResourceID(org, dest.Name))
	data.DestinationType = types.StringValue(dest.DestinationType)
	data.Template = stringFromPtr(dest.Template)
	data.URL = types.StringValue(dest.URL)
	data.Method = types.StringValue(dest.Method)
	data.SkipTLSVerify = types.BoolValue(dest.SkipTLSVerify)
	data.Emails = setFromStrings(ctx, dest.Emails, &resp.Diagnostics)
	data.SNSTopicARN = stringFromPtr(dest.SNSTopicARN)
	data.AWSRegion = stringFromPtr(dest.AWSRegion)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewAlertDestinationsDataSource returns a factory for the openobserve_alert_destinations data source.
func NewAlertDestinationsDataSource() datasource.DataSource { return &AlertDestinationsDataSource{} }

// AlertDestinationsDataSource lists the alert destinations of an organization.
type AlertDestinationsDataSource struct{ client *Client }

// AlertDestinationsDataSourceModel holds the state for a destination listing.
type AlertDestinationsDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	OrgID        types.String `tfsdk:"org_id"`
	Destinations types.List   `tfsdk:"destinations"`
}

var alertDestinationAttrTypes = map[string]attr.Type{
	"name":     types.StringType,
	"type":     types.StringType,
	"template": types.StringType,
	"url":      types.StringType,
	"method":   types.StringType,
}

func (d *AlertDestinationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_destinations"
}

func (d *AlertDestinationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the alert destinations of an OpenObserve organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"destinations": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The destinations found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":     schema.StringAttribute{Computed: true, Description: "Destination name."},
						"type":     schema.StringAttribute{Computed: true, Description: "Delivery channel."},
						"template": schema.StringAttribute{Computed: true, Description: "Template used to render the message."},
						"url":      schema.StringAttribute{Computed: true, Description: "Webhook URL."},
						"method":   schema.StringAttribute{Computed: true, Description: "HTTP method used for webhook delivery."},
					},
				},
			},
		},
	}
}

func (d *AlertDestinationsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertDestinationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertDestinationsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	destinations, err := d.client.ListAlertDestinations(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error listing alert destinations", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(destinations))
	for _, dst := range destinations {
		values = append(values, objectValue(alertDestinationAttrTypes, map[string]attr.Value{
			"name":     types.StringValue(dst.Name),
			"type":     types.StringValue(dst.DestinationType),
			"template": stringFromPtr(dst.Template),
			"url":      types.StringValue(dst.URL),
			"method":   types.StringValue(dst.Method),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: alertDestinationAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/alerts/destinations", org))
	data.Destinations = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------------

// NewAlertDataSource returns a factory for the openobserve_alert data source.
func NewAlertDataSource() datasource.DataSource { return &AlertDataSource{} }

// AlertDataSource reads a single alert by ID or by name.
type AlertDataSource struct{ client *Client }

// AlertDataSourceModel holds the state for an alert lookup.
type AlertDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	OrgID        types.String `tfsdk:"org_id"`
	AlertID      types.String `tfsdk:"alert_id"`
	Name         types.String `tfsdk:"name"`
	FolderID     types.String `tfsdk:"folder_id"`
	StreamType   types.String `tfsdk:"stream_type"`
	StreamName   types.String `tfsdk:"stream_name"`
	Description  types.String `tfsdk:"description"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	IsRealTime   types.Bool   `tfsdk:"is_real_time"`
	Owner        types.String `tfsdk:"owner"`
	Destinations types.Set    `tfsdk:"destinations"`
	Tags         types.Set    `tfsdk:"tags"`
}

func (d *AlertDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert"
}

func (d *AlertDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing alert, looked up either by `alert_id` or by `name`.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{alert_id}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"alert_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Alert identifier. Supply this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Alert name. Supply this or `alert_id`.",
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Restrict a name lookup to one folder. All folders are searched when omitted.",
			},
			"stream_type":  schema.StringAttribute{Computed: true, Description: "Stream type the alert queries."},
			"stream_name":  schema.StringAttribute{Computed: true, Description: "Stream the alert queries."},
			"description":  schema.StringAttribute{Computed: true, Description: "What the alert monitors."},
			"enabled":      schema.BoolAttribute{Computed: true, Description: "Whether the alert is evaluated."},
			"is_real_time": schema.BoolAttribute{Computed: true, Description: "Whether the alert evaluates as data arrives."},
			"owner":        schema.StringAttribute{Computed: true, Description: "Alert owner."},
			"destinations": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Destinations notified when the alert fires.",
			},
			"tags": schema.SetAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "Selection tags on the alert.",
			},
		},
	}
}

func (d *AlertDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	alertID := data.AlertID.ValueString()
	folderID := ""
	if alertID == "" {
		name := data.Name.ValueString()
		if name == "" {
			resp.Diagnostics.AddError(
				"Missing alert selector",
				"Set either `alert_id` or `name` to identify the alert.",
			)
			return
		}
		found, err := d.client.FindAlertByName(ctx, org, data.FolderID.ValueString(), name)
		if err != nil {
			resp.Diagnostics.AddError("Error searching for alert", err.Error())
			return
		}
		if found == nil {
			resp.Diagnostics.AddError("Alert not found", fmt.Sprintf("No alert named %q in org %q.", name, org))
			return
		}
		alertID = found.AlertID
		folderID = found.FolderID
	}

	alert, err := d.client.GetAlert(ctx, org, alertID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading alert", err.Error())
		return
	}
	if alert == nil {
		resp.Diagnostics.AddError("Alert not found", fmt.Sprintf("Alert %q not found in org %q.", alertID, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.AlertID = types.StringValue(alertID)
	data.ID = types.StringValue(alertResourceID(org, alertID))
	data.Name = types.StringValue(alert.Name)
	data.StreamType = types.StringValue(alert.StreamType)
	data.StreamName = types.StringValue(alert.StreamName)
	data.Description = types.StringValue(alert.Description)
	data.Enabled = types.BoolValue(alert.Enabled)
	data.IsRealTime = types.BoolValue(alert.IsRealTime)
	data.Owner = stringFromPtr(alert.Owner)
	data.Destinations = setFromStrings(ctx, alert.Destinations, &resp.Diagnostics)
	data.Tags = setFromStrings(ctx, alert.Tags, &resp.Diagnostics)

	if folderID == "" && alert.FolderID != nil {
		folderID = *alert.FolderID
	}
	if folderID != "" {
		data.FolderID = types.StringValue(folderID)
	} else if data.FolderID.IsNull() {
		data.FolderID = types.StringValue("")
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewAlertsDataSource returns a factory for the openobserve_alerts data source.
func NewAlertsDataSource() datasource.DataSource { return &AlertsDataSource{} }

// AlertsDataSource lists the alerts of an organization.
type AlertsDataSource struct{ client *Client }

// AlertsDataSourceModel holds the state for an alert listing.
type AlertsDataSourceModel struct {
	ID       types.String `tfsdk:"id"`
	OrgID    types.String `tfsdk:"org_id"`
	FolderID types.String `tfsdk:"folder_id"`
	Alerts   types.List   `tfsdk:"alerts"`
}

var alertSummaryAttrTypes = map[string]attr.Type{
	"alert_id":     types.StringType,
	"name":         types.StringType,
	"folder_id":    types.StringType,
	"folder_name":  types.StringType,
	"alert_type":   types.StringType,
	"owner":        types.StringType,
	"description":  types.StringType,
	"enabled":      types.BoolType,
	"is_real_time": types.BoolType,
}

func (d *AlertsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alerts"
}

func (d *AlertsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the alerts of an OpenObserve organization, optionally scoped to one folder.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id":    schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"folder_id": schema.StringAttribute{Optional: true, Description: "Restrict the listing to one alert folder."},
			"alerts": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The alerts found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"alert_id":     schema.StringAttribute{Computed: true, Description: "Alert identifier."},
						"name":         schema.StringAttribute{Computed: true, Description: "Alert name."},
						"folder_id":    schema.StringAttribute{Computed: true, Description: "Folder holding the alert."},
						"folder_name":  schema.StringAttribute{Computed: true, Description: "Name of that folder."},
						"alert_type":   schema.StringAttribute{Computed: true, Description: "Alert type reported by the server."},
						"owner":        schema.StringAttribute{Computed: true, Description: "Alert owner."},
						"description":  schema.StringAttribute{Computed: true, Description: "What the alert monitors."},
						"enabled":      schema.BoolAttribute{Computed: true, Description: "Whether the alert is evaluated."},
						"is_real_time": schema.BoolAttribute{Computed: true, Description: "Whether the alert evaluates as data arrives."},
					},
				},
			},
		},
	}
}

func (d *AlertsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *AlertsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data AlertsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	alerts, err := d.client.ListAlerts(ctx, org, data.FolderID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error listing alerts", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(alerts))
	for _, a := range alerts {
		values = append(values, objectValue(alertSummaryAttrTypes, map[string]attr.Value{
			"alert_id":     types.StringValue(a.AlertID),
			"name":         types.StringValue(a.Name),
			"folder_id":    types.StringValue(a.FolderID),
			"folder_name":  types.StringValue(a.FolderName),
			"alert_type":   types.StringValue(a.AlertType),
			"owner":        stringFromPtr(a.Owner),
			"description":  stringFromPtr(a.Description),
			"enabled":      types.BoolValue(a.Enabled),
			"is_real_time": types.BoolValue(a.IsRealTime),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: alertSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/alerts", org))
	data.Alerts = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
