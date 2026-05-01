package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/openobserve/terraform-provider-openobserve/internal/provider"
)

// version is set at build time by goreleaser via -ldflags.
var version string = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "run the provider with debug support (for use with delve or similar)")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/openobserve/openobserve",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)
	if err != nil {
		log.Fatal(err.Error())
	}
}
