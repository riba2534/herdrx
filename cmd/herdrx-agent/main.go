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
	// 兼容包装：直接复用 agentcli 的可测试总分发逻辑，不复制两套实现
	os.Exit(agentcli.Run(os.Args[1:], os.Stdout, os.Stderr, agentcli.DefaultEnv()))
}
