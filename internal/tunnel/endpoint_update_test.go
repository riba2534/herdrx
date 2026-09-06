package tunnel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/secure"
	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestEndpointPacketRejectsExpiryTamperingAndTrailingData(t *testing.T) {
	private, public, err := secure.GenerateSSHKey("endpoint-test")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	node := tailcat.NewPrivateKey()
	node.Public.Region = []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{HostName: "203.0.113.20"}}}}
	payload := EndpointUpdate{Version: 1, AgentID: "agent", ControllerID: "controller", BindingID: "binding", ClientNode: key.NewNode().Public().String(), Address: string(node.Public.Addr()), Revision: 1, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	identity := EndpointIdentity{AgentID: payload.AgentID, ControllerID: payload.ControllerID, BindingID: payload.BindingID, ClientNode: payload.ClientNode, Address: payload.Address, SSHHostKey: string(public)}
	signRaw := func(p EndpointUpdate, mutate bool, trailing string) string {
		t.Helper()
		data, _ := json.Marshal(p)
		sig, err := signer.Sign(rand.Reader, append([]byte(endpointSignatureDomain), data...))
		if err != nil {
			t.Fatal(err)
		}
		if mutate {
			p.Revision++
		}
		encoded, _ := json.Marshal(endpointEnvelope{Payload: p, Signature: base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
		encoded = append(encoded, []byte(trailing)...)
		return EndpointUpdatePrefix + base64.RawURLEncoding.EncodeToString(encoded)
	}
	if _, err := VerifyEndpointUpdate(signRaw(payload, false, ""), identity); err != nil {
		t.Fatal(err)
	}
	expired := payload
	expired.CreatedAt = time.Now().Add(-time.Hour).Unix()
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	future := payload
	future.CreatedAt = time.Now().Add(time.Hour).Unix()
	future.ExpiresAt = future.CreatedAt + 600
	for _, packet := range []string{signRaw(expired, false, ""), signRaw(future, false, ""), signRaw(payload, true, ""), signRaw(payload, false, "{}"), PrefixV1 + "wrong-protocol"} {
		if _, err := VerifyEndpointUpdate(packet, identity); err == nil {
			t.Fatal("invalid endpoint packet accepted")
		}
	}
}
