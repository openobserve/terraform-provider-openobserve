//go:build tools

package tools

import (
	// ensure goreleaser and tfplugindocs are available in go module graph
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
