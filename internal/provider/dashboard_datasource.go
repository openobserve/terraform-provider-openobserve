package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSourceWithConfigure = &FolderDataSource{}
	_ datasource.DataSourceWithConfigure = &FoldersDataSource{}
	_ datasource.DataSourceWithConfigure = &DashboardDataSource{}
	_ datasource.DataSourceWithConfigure = &DashboardsDataSource{}
)

// ---------------------------------------------------------------------------
// Folders
// ---------------------------------------------------------------------------

// NewFolderDataSource returns a factory for the openobserve_folder data source.
func NewFolderDataSource() datasource.DataSource { return &FolderDataSource{} }

// FolderDataSource reads a single folder by ID or by name.
type FolderDataSource struct{ client *Client }

// FolderDataSourceModel holds the state for a folder lookup.
type FolderDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	OrgID       types.String `tfsdk:"org_id"`
	FolderType  types.String `tfsdk:"folder_type"`
	FolderID    types.String `tfsdk:"folder_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (d *FolderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_folder"
}

func (d *FolderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a folder, looked up either by `folder_id` or by `name`. Useful for placing dashboards and " +
			"alerts into folders that already exist.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{folder_type}/{folder_id}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"folder_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "What the folder holds: `dashboards` (default), `alerts`, `reports`, or `synthetics`.",
				Validators:  []validator.String{stringvalidator.OneOf(folderTypes...)},
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Folder identifier. Supply this or `name`.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Folder name. Supply this or `folder_id`.",
			},
			"description": schema.StringAttribute{Computed: true, Description: "Folder description."},
		},
	}
}

func (d *FolderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *FolderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data FolderDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderType := data.FolderType.ValueString()
	if folderType == "" {
		folderType = "dashboards"
	}

	var folder *FolderAPI
	var err error
	switch {
	case data.FolderID.ValueString() != "":
		folder, err = d.client.GetFolder(ctx, org, folderType, data.FolderID.ValueString())
	case data.Name.ValueString() != "":
		folder, err = d.client.GetFolderByName(ctx, org, folderType, data.Name.ValueString())
	default:
		resp.Diagnostics.AddError(
			"Missing folder selector",
			"Set either `folder_id` or `name` to identify the folder.",
		)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading folder", err.Error())
		return
	}
	if folder == nil {
		resp.Diagnostics.AddError(
			"Folder not found",
			fmt.Sprintf("No %s folder matching the given selector exists in org %q.", folderType, org),
		)
		return
	}

	folderID := folder.FolderID
	if folderID == "" {
		folderID = data.FolderID.ValueString()
	}

	data.OrgID = types.StringValue(org)
	data.FolderType = types.StringValue(folderType)
	data.FolderID = types.StringValue(folderID)
	data.Name = types.StringValue(folder.Name)
	data.Description = types.StringValue(folder.Description)
	data.ID = types.StringValue(folderResourceID(org, folderType, folderID))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewFoldersDataSource returns a factory for the openobserve_folders data source.
func NewFoldersDataSource() datasource.DataSource { return &FoldersDataSource{} }

// FoldersDataSource lists the folders of one type.
type FoldersDataSource struct{ client *Client }

// FoldersDataSourceModel holds the state for a folder listing.
type FoldersDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	OrgID      types.String `tfsdk:"org_id"`
	FolderType types.String `tfsdk:"folder_type"`
	Folders    types.List   `tfsdk:"folders"`
}

var folderAttrTypes = map[string]attr.Type{
	"folder_id":   types.StringType,
	"name":        types.StringType,
	"description": types.StringType,
}

func (d *FoldersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_folders"
}

func (d *FoldersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the folders of one type in an OpenObserve organization.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"folder_type": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "What the folders hold: `dashboards` (default), `alerts`, `reports`, or `synthetics`.",
				Validators:  []validator.String{stringvalidator.OneOf(folderTypes...)},
			},
			"folders": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The folders found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"folder_id":   schema.StringAttribute{Computed: true, Description: "Folder identifier."},
						"name":        schema.StringAttribute{Computed: true, Description: "Folder name."},
						"description": schema.StringAttribute{Computed: true, Description: "Folder description."},
					},
				},
			},
		},
	}
}

func (d *FoldersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *FoldersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data FoldersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	folderType := data.FolderType.ValueString()
	if folderType == "" {
		folderType = "dashboards"
	}

	folders, err := d.client.ListFolders(ctx, org, folderType)
	if err != nil {
		resp.Diagnostics.AddError("Error listing folders", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(folders))
	for _, f := range folders {
		values = append(values, objectValue(folderAttrTypes, map[string]attr.Value{
			"folder_id":   types.StringValue(f.FolderID),
			"name":        types.StringValue(f.Name),
			"description": types.StringValue(f.Description),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: folderAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.FolderType = types.StringValue(folderType)
	data.ID = types.StringValue(fmt.Sprintf("%s/folders/%s", org, folderType))
	data.Folders = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---------------------------------------------------------------------------
// Dashboards
// ---------------------------------------------------------------------------

// NewDashboardDataSource returns a factory for the openobserve_dashboard data source.
func NewDashboardDataSource() datasource.DataSource { return &DashboardDataSource{} }

// DashboardDataSource reads a single dashboard by ID or by title.
type DashboardDataSource struct{ client *Client }

// DashboardDataSourceModel holds the state for a dashboard lookup.
type DashboardDataSourceModel struct {
	ID            types.String `tfsdk:"id"`
	OrgID         types.String `tfsdk:"org_id"`
	DashboardID   types.String `tfsdk:"dashboard_id"`
	Title         types.String `tfsdk:"title"`
	FolderID      types.String `tfsdk:"folder_id"`
	DashboardJSON types.String `tfsdk:"dashboard_json"`
	Description   types.String `tfsdk:"description"`
	Owner         types.String `tfsdk:"owner"`
	Role          types.String `tfsdk:"role"`
	Version       types.Int64  `tfsdk:"version"`
	Hash          types.String `tfsdk:"hash"`
	UpdatedAt     types.Int64  `tfsdk:"updated_at"`
}

func (d *DashboardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (d *DashboardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads an existing dashboard, looked up either by `dashboard_id` or by `title`. The full document " +
			"is returned in `dashboard_json`, which makes this a convenient way to copy a dashboard built in the UI " +
			"into Terraform.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID in the format `{org_id}/{dashboard_id}`."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"dashboard_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Dashboard identifier. Supply this or `title`.",
			},
			"title": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Dashboard title. Supply this or `dashboard_id`.",
			},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Restrict a title lookup to one folder. All folders are searched when omitted.",
			},
			"dashboard_json": schema.StringAttribute{Computed: true, Description: "The dashboard document as JSON."},
			"description":    schema.StringAttribute{Computed: true, Description: "Dashboard description."},
			"owner":          schema.StringAttribute{Computed: true, Description: "Dashboard owner."},
			"role":           schema.StringAttribute{Computed: true, Description: "Role associated with the dashboard."},
			"version":        schema.Int64Attribute{Computed: true, Description: "Dashboard schema version."},
			"hash":           schema.StringAttribute{Computed: true, Description: "Concurrency token from the last write."},
			"updated_at":     schema.Int64Attribute{Computed: true, Description: "Timestamp of the last modification, in microseconds."},
		},
	}
}

func (d *DashboardDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *DashboardDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DashboardDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	dashboardID := data.DashboardID.ValueString()
	folderID := data.FolderID.ValueString()
	if dashboardID == "" {
		title := data.Title.ValueString()
		if title == "" {
			resp.Diagnostics.AddError(
				"Missing dashboard selector",
				"Set either `dashboard_id` or `title` to identify the dashboard.",
			)
			return
		}
		found, err := d.client.FindDashboardByTitle(ctx, org, folderID, title)
		if err != nil {
			resp.Diagnostics.AddError("Error searching for dashboard", err.Error())
			return
		}
		if found == nil {
			resp.Diagnostics.AddError("Dashboard not found", fmt.Sprintf("No dashboard titled %q in org %q.", title, org))
			return
		}
		dashboardID = found.DashboardID
		folderID = found.FolderID
	}

	dashboard, err := d.client.GetDashboard(ctx, org, dashboardID)
	if err != nil {
		resp.Diagnostics.AddError("Error reading dashboard", err.Error())
		return
	}
	if dashboard == nil {
		resp.Diagnostics.AddError("Dashboard not found", fmt.Sprintf("Dashboard %q not found in org %q.", dashboardID, org))
		return
	}

	data.OrgID = types.StringValue(org)
	data.DashboardID = types.StringValue(dashboardID)
	data.ID = types.StringValue(dashboardResourceID(org, dashboardID))
	data.DashboardJSON = jsonStringValue(dashboard.Body(), &resp.Diagnostics)
	data.Title = types.StringValue(dashboard.Title())
	data.Description = types.StringValue(dashboard.Description())
	data.Owner = types.StringValue(dashboard.Owner())
	data.Role = types.StringValue(dashboard.Role())
	data.Version = types.Int64Value(int64(dashboard.Version))
	data.Hash = types.StringValue(dashboard.Hash)
	data.UpdatedAt = types.Int64Value(dashboard.UpdatedAt)
	data.FolderID = types.StringValue(folderID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// NewDashboardsDataSource returns a factory for the openobserve_dashboards data source.
func NewDashboardsDataSource() datasource.DataSource { return &DashboardsDataSource{} }

// DashboardsDataSource lists the dashboards of an organization.
type DashboardsDataSource struct{ client *Client }

// DashboardsDataSourceModel holds the state for a dashboard listing.
type DashboardsDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	OrgID      types.String `tfsdk:"org_id"`
	FolderID   types.String `tfsdk:"folder_id"`
	Dashboards types.List   `tfsdk:"dashboards"`
}

var dashboardSummaryAttrTypes = map[string]attr.Type{
	"dashboard_id": types.StringType,
	"title":        types.StringType,
	"description":  types.StringType,
	"folder_id":    types.StringType,
	"folder_name":  types.StringType,
	"owner":        types.StringType,
	"version":      types.Int64Type,
	"updated_at":   types.Int64Type,
}

func (d *DashboardsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboards"
}

func (d *DashboardsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the dashboards of an OpenObserve organization, optionally scoped to one folder.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Computed: true, Description: "Data source ID."},
			"org_id": schema.StringAttribute{Optional: true, Computed: true, Description: "Organization identifier. Defaults to the provider's `org_id`."},
			"folder_id": schema.StringAttribute{
				Optional:    true,
				Description: "Restrict the listing to one folder. Omitting it lists the `default` folder, matching the API's own behaviour.",
			},
			"dashboards": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The dashboards found.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"dashboard_id": schema.StringAttribute{Computed: true, Description: "Dashboard identifier."},
						"title":        schema.StringAttribute{Computed: true, Description: "Dashboard title."},
						"description":  schema.StringAttribute{Computed: true, Description: "Dashboard description."},
						"folder_id":    schema.StringAttribute{Computed: true, Description: "Folder holding the dashboard."},
						"folder_name":  schema.StringAttribute{Computed: true, Description: "Name of that folder."},
						"owner":        schema.StringAttribute{Computed: true, Description: "Dashboard owner."},
						"version":      schema.Int64Attribute{Computed: true, Description: "Dashboard schema version."},
						"updated_at":   schema.Int64Attribute{Computed: true, Description: "Timestamp of the last modification."},
					},
				},
			},
		},
	}
}

func (d *DashboardsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.client = configureDataSourceClient(req, resp)
}

func (d *DashboardsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data DashboardsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := requireOrg(d.client, data.OrgID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	dashboards, err := d.client.ListDashboards(ctx, org, data.FolderID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error listing dashboards", err.Error())
		return
	}

	values := make([]attr.Value, 0, len(dashboards))
	for _, item := range dashboards {
		values = append(values, objectValue(dashboardSummaryAttrTypes, map[string]attr.Value{
			"dashboard_id": types.StringValue(item.DashboardID),
			"title":        types.StringValue(item.Title),
			"description":  types.StringValue(item.Description),
			"folder_id":    types.StringValue(item.FolderID),
			"folder_name":  types.StringValue(item.FolderName),
			"owner":        types.StringValue(item.Owner),
			"version":      types.Int64Value(int64(item.Version)),
			"updated_at":   types.Int64Value(item.UpdatedAt),
		}, &resp.Diagnostics))
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: dashboardSummaryAttrTypes}, values)
	resp.Diagnostics.Append(diags...)

	data.OrgID = types.StringValue(org)
	data.ID = types.StringValue(fmt.Sprintf("%s/dashboards", org))
	data.Dashboards = list

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
