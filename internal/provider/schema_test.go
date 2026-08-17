package provider_test

import (
	"context"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/openobserve/terraform-provider-openobserve/internal/provider"
)

// newProvider builds the provider under test.
func newProvider() fwprovider.Provider {
	return provider.New("test")()
}

// TestResourceSchemas walks every registered resource and asserts its schema is
// well formed. Terraform only surfaces these errors at plan time against a real
// server, so checking them here keeps a malformed schema from reaching a user.
func TestResourceSchemas(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, factory := range newProvider().Resources(ctx) {
		res := factory()

		metadataResp := &fwresource.MetadataResponse{}
		res.Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "openobserve"}, metadataResp)

		t.Run(metadataResp.TypeName, func(t *testing.T) {
			schemaResp := &fwresource.SchemaResponse{}
			res.Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
			if schemaResp.Diagnostics.HasError() {
				t.Fatalf("schema returned errors: %+v", schemaResp.Diagnostics)
			}

			diags := schemaResp.Schema.ValidateImplementation(ctx)
			if diags.HasError() {
				t.Fatalf("schema is not a valid implementation: %+v", diags)
			}

			if _, ok := schemaResp.Schema.Attributes["id"]; !ok {
				t.Error("every resource needs an `id` attribute so Terraform can address it")
			}
			if schemaResp.Schema.Description == "" {
				t.Error("resource is missing a description")
			}
		})
	}
}

// TestDataSourceSchemas does the same for every registered data source.
func TestDataSourceSchemas(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, factory := range newProvider().DataSources(ctx) {
		ds := factory()

		metadataResp := &fwdatasource.MetadataResponse{}
		ds.Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "openobserve"}, metadataResp)

		t.Run(metadataResp.TypeName, func(t *testing.T) {
			schemaResp := &fwdatasource.SchemaResponse{}
			ds.Schema(ctx, fwdatasource.SchemaRequest{}, schemaResp)
			if schemaResp.Diagnostics.HasError() {
				t.Fatalf("schema returned errors: %+v", schemaResp.Diagnostics)
			}

			diags := schemaResp.Schema.ValidateImplementation(ctx)
			if diags.HasError() {
				t.Fatalf("schema is not a valid implementation: %+v", diags)
			}

			if schemaResp.Schema.Description == "" {
				t.Error("data source is missing a description")
			}
		})
	}
}

// TestResourceTypeNamesAreUnique guards against two resources claiming the same
// Terraform type name, which would make one of them unreachable.
func TestResourceTypeNamesAreUnique(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	seen := map[string]bool{}
	for _, factory := range newProvider().Resources(ctx) {
		resp := &fwresource.MetadataResponse{}
		factory().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "openobserve"}, resp)
		if seen[resp.TypeName] {
			t.Errorf("resource type %q is registered more than once", resp.TypeName)
		}
		seen[resp.TypeName] = true
		if !strings.HasPrefix(resp.TypeName, "openobserve_") {
			t.Errorf("resource type %q does not carry the provider prefix", resp.TypeName)
		}
	}
}

// TestDataSourceTypeNamesAreUnique does the same for data sources.
func TestDataSourceTypeNamesAreUnique(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	seen := map[string]bool{}
	for _, factory := range newProvider().DataSources(ctx) {
		resp := &fwdatasource.MetadataResponse{}
		factory().Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "openobserve"}, resp)
		if seen[resp.TypeName] {
			t.Errorf("data source type %q is registered more than once", resp.TypeName)
		}
		seen[resp.TypeName] = true
		if !strings.HasPrefix(resp.TypeName, "openobserve_") {
			t.Errorf("data source type %q does not carry the provider prefix", resp.TypeName)
		}
	}
}

// TestExpectedResourcesAreRegistered pins the resource surface, so dropping one
// by accident during a refactor fails the build.
func TestExpectedResourcesAreRegistered(t *testing.T) {
	t.Parallel()

	expected := []string{
		"openobserve_alert",
		"openobserve_alert_destination",
		"openobserve_alert_template",
		"openobserve_dashboard",
		"openobserve_folder",
		"openobserve_group",
		"openobserve_organization",
		"openobserve_role",
		"openobserve_service_account",
		"openobserve_slo",
		"openobserve_stream",
		"openobserve_user",
	}

	ctx := context.Background()
	registered := map[string]bool{}
	for _, factory := range newProvider().Resources(ctx) {
		resp := &fwresource.MetadataResponse{}
		factory().Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "openobserve"}, resp)
		registered[resp.TypeName] = true
	}

	for _, name := range expected {
		if !registered[name] {
			t.Errorf("resource %q is not registered with the provider", name)
		}
	}
}

// TestExpectedDataSourcesAreRegistered pins the data source surface.
func TestExpectedDataSourcesAreRegistered(t *testing.T) {
	t.Parallel()

	expected := []string{
		"openobserve_alert",
		"openobserve_alert_destination",
		"openobserve_alert_destinations",
		"openobserve_alert_template",
		"openobserve_alert_templates",
		"openobserve_alerts",
		"openobserve_dashboard",
		"openobserve_dashboards",
		"openobserve_folder",
		"openobserve_folders",
		"openobserve_group",
		"openobserve_groups",
		"openobserve_organization",
		"openobserve_organizations",
		"openobserve_resources",
		"openobserve_role",
		"openobserve_roles",
		"openobserve_service_accounts",
		"openobserve_slo",
		"openobserve_slos",
		"openobserve_stream",
		"openobserve_streams",
		"openobserve_user",
		"openobserve_user_roles",
		"openobserve_users",
	}

	ctx := context.Background()
	registered := map[string]bool{}
	for _, factory := range newProvider().DataSources(ctx) {
		resp := &fwdatasource.MetadataResponse{}
		factory().Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "openobserve"}, resp)
		registered[resp.TypeName] = true
	}

	for _, name := range expected {
		if !registered[name] {
			t.Errorf("data source %q is not registered with the provider", name)
		}
	}
}
