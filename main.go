package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/TakatoHano/terraform-provider-mysql/mysql"
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
)

//go:generate go tool tfplugindocs generate --provider-name mysql --rendered-provider-name "MySQL (TakatoHano fork)"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: mysql.Provider,
		Debug:        debug,
		ProviderAddr: "registry.terraform.io/TakatoHano/mysql",
	})

	// plugin.Serve returns once Terraform shuts the plugin down; release the
	// SSH tunnel and SSM session before the process exits.
	if err := mysql.Cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup failed:", err)
	}
}
