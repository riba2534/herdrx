package agentcli

import (
	"fmt"
	"io"

	"github.com/riba2534/herdrx/internal/tunnel"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

func readDERPConfig(path string) (*tailcfg.DERPRegion, error) {
	file, err := openUpdateFile(path, tunnel.MaxConnectionStringLen)
	if err != nil {
		return nil, fmt.Errorf("read DERP config: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tunnel.MaxConnectionStringLen+1))
	if err != nil {
		return nil, err
	}
	return tunnel.ParseDERPConfig(data)
}
func sameDERPRegion(cfg Config, region *tailcfg.DERPRegion) bool {
	if region == nil {
		return true
	}
	previous := tailcat.ConnInfo{Region: cfg.Node.Public.Region}
	next := tailcat.ConnInfo{Region: []*tailcfg.DERPRegion{region}}
	return previous.Addr() == next.Addr()
}
func applyDERPRegion(cfg *Config, region *tailcfg.DERPRegion) {
	if region != nil {
		cfg.Node.Public.Region = []*tailcfg.DERPRegion{region}
		cfg.Node.Public.RegionID = 0
	}
}
