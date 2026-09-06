package tunnel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"tailscale.com/tailcfg"
)

// ParseDERPConfig accepts one explicit region, never a URL or arbitrary map
// fetch. All node addresses are validated and pinned before use.
func ParseDERPConfig(data []byte) (*tailcfg.DERPRegion, error) {
	if len(data) > MaxConnectionStringLen {
		return nil, errors.New("DERP configuration exceeds 16 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var region tailcfg.DERPRegion
	if err := decoder.Decode(&region); err != nil {
		return nil, fmt.Errorf("decode DERP configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("DERP configuration has trailing data")
	}
	return DefaultSSRFValidator.ValidateDERPRegion(&region)
}
func (v *SSRFValidator) ValidateDERPRegion(region *tailcfg.DERPRegion) (*tailcfg.DERPRegion, error) {
	if region == nil || region.RegionID <= 0 || region.RegionID > 65535 || len(region.Nodes) == 0 || len(region.Nodes) > 8 {
		return nil, errors.New("DERP region requires ID 1–65535 and 1–8 nodes")
	}
	encoded, err := json.Marshal(region)
	if err != nil || len(encoded) > MaxConnectionStringLen {
		return nil, errors.New("invalid or oversized DERP region")
	}
	var result tailcfg.DERPRegion
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	if len(result.RegionCode) > 64 || len(result.RegionName) > 128 {
		return nil, errors.New("DERP region name is too long")
	}
	names := make(map[string]bool)
	relays := 0
	for i, node := range result.Nodes {
		if node == nil {
			return nil, errors.New("DERP node cannot be null")
		}
		if node.Name == "" {
			node.Name = fmt.Sprintf("herdrx-%d", i+1)
		}
		if len(node.Name) > 64 || strings.ContainsAny(node.Name, " \t\r\n") || names[node.Name] {
			return nil, errors.New("DERP node names must be unique and at most 64 characters")
		}
		names[node.Name] = true
		node.RegionID = result.RegionID
		if !node.STUNOnly {
			relays++
		}
		if err := v.validateDERPNode(node); err != nil {
			return nil, err
		}
	}
	if relays == 0 {
		return nil, errors.New("DERP region needs at least one relay node")
	}
	return &result, nil
}
