package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/openobserve/terraform-provider-openobserve/internal/provider"
)

// testAccProtoV6ProviderFactories is used in acceptance tests to instantiate
// a fresh provider for each test. The address must match main.go.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"openobserve": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func TestProvider(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Provider configuration is minimal; real acceptance tests
				// require OPENOBSERVE_ENDPOINT, _USERNAME, _PASSWORD env vars.
				Config: `provider "openobserve" {
  endpoint = "http://localhost:5080"
  username = "root@example.com"
  password = "Complexpass#123"
  org_id   = "default"
}`,
			},
		},
	})
}
