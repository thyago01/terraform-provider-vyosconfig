package main

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/thyago01/terraform-provider-vyosconfig/vyos"
)

func main() {
	providerserver.Serve(context.Background(), vyos.New, providerserver.ServeOpts{
		Address: "registry.terraform.io/thyago01/vyosconfig",
	})
}
