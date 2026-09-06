package main

import (
	"os"

	"github.com/riba2534/herdrx/internal/agentcli"
)

var version = "dev"

func main() {
	if version != "dev" {
		agentcli.Version = version
	}
	os.Exit(agentcli.Run(os.Args[1:], os.Stdout, os.Stderr, agentcli.DefaultEnv()))
}
