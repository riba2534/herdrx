package tunnel

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
)

const EndpointUpdatePrefix = "herdrx://endpoint-v1/"
const endpointSignatureDomain = "herdrx endpoint update v1\x00"

type EndpointUpdate struct {
	Version      int    `json:"v"`
	AgentID      string `json:"agent_id"`
	ControllerID string `json:"controller_id"`
	BindingID    string `json:"binding_id"`
	ClientNode   string `json:"client_node"`
	Address      string `json:"tc"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"created_at"`
	ExpiresAt    int64  `json:"expires_at"`
}
type endpointEnvelope struct {
	Payload   EndpointUpdate `json:"payload"`
	Signature string         `json:"signature"`
}
type EndpointIdentity struct {
	AgentID, ControllerID, BindingID, ClientNode, Address, SSHHostKey string
	MinimumVersion                                                    int64
}

func BuildEndpointUpdate(payload EndpointUpdate, signer ssh.Signer) (string, error) {
	if err := validateEndpointPayload(payload, time.Now()); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signature, err := signer.Sign(rand.Reader, append([]byte(endpointSignatureDomain), encoded...))
	if err != nil {
		return "", err
	}
	envelope, err := json.Marshal(endpointEnvelope{Payload: payload, Signature: base64.RawURLEncoding.EncodeToString(ssh.Marshal(signature))})
	if err != nil {
		return "", err
	}
	result := EndpointUpdatePrefix + base64.RawURLEncoding.EncodeToString(envelope)
	if len(result) > MaxConnectionStringLen {
		return "", errors.New("endpoint update exceeds 16 KiB")
	}
	return result, nil
}

// VerifyEndpointUpdate authenticates every field before any network lookup.
// The caller must then validate/pin the new address and atomically compare the
// previous encrypted credential when saving its monotonically newer revision.
func VerifyEndpointUpdate(raw string, expected EndpointIdentity) (EndpointUpdate, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxConnectionStringLen || !strings.HasPrefix(raw, EndpointUpdatePrefix) {
		return EndpointUpdate{}, errors.New("invalid endpoint update prefix or length")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, EndpointUpdatePrefix))
	if err != nil {
		return EndpointUpdate{}, errors.New("invalid endpoint update encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope endpointEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return EndpointUpdate{}, fmt.Errorf("invalid endpoint update: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return EndpointUpdate{}, errors.New("endpoint update has trailing data")
	}
	payload := envelope.Payload
	if err := validateEndpointPayload(payload, time.Now()); err != nil {
		return EndpointUpdate{}, err
	}
	hostKey, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(expected.SSHHostKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return EndpointUpdate{}, errors.New("stored SSH host key is invalid")
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return EndpointUpdate{}, errors.New("invalid endpoint signature encoding")
	}
	var signature ssh.Signature
	if err := ssh.Unmarshal(signatureBytes, &signature); err != nil {
		return EndpointUpdate{}, errors.New("invalid endpoint signature")
	}
	signed, _ := json.Marshal(payload)
	if err := hostKey.Verify(append([]byte(endpointSignatureDomain), signed...), &signature); err != nil {
		return EndpointUpdate{}, errors.New("endpoint signature does not match this host")
	}
	if payload.AgentID != expected.AgentID || payload.ControllerID != expected.ControllerID || payload.BindingID != expected.BindingID || payload.ClientNode != expected.ClientNode {
		return EndpointUpdate{}, errors.New("endpoint update belongs to a different agent or binding")
	}
	if payload.Revision <= expected.MinimumVersion {
		return EndpointUpdate{}, errors.New("endpoint update is stale or already imported")
	}
	previous, err := tailcat.ParseAddr(tailcat.Addr(expected.Address))
	if err != nil {
		return EndpointUpdate{}, errors.New("stored formal address is invalid")
	}
	next, err := tailcat.ParseAddr(tailcat.Addr(payload.Address))
	if err != nil {
		return EndpointUpdate{}, errors.New("new formal address is invalid")
	}
	if previous.PresharedKey.IsZero() || next.PresharedKey.IsZero() || previous.PresharedKey != next.PresharedKey || !previous.ServerPublic.Equal(next.ServerPublic) || !previous.ServerDiscoPublic.Equal(next.ServerDiscoPublic) {
		return EndpointUpdate{}, errors.New("endpoint update cannot change the host identity or PSK")
	}
	return payload, nil
}
func validateEndpointPayload(p EndpointUpdate, now time.Time) error {
	if p.Version != 1 || p.Revision <= 0 {
		return errors.New("invalid endpoint update version")
	}
	for _, field := range []string{p.AgentID, p.ControllerID, p.BindingID, p.ClientNode} {
		if field == "" || len(field) > 128 || strings.ContainsAny(field, "\x00\r\n") {
			return errors.New("invalid endpoint identity")
		}
	}
	if p.Address == "" || len(p.Address) > 12000 {
		return errors.New("invalid endpoint address length")
	}
	if p.CreatedAt <= 0 || p.CreatedAt > now.Add(5*time.Minute).Unix() || p.ExpiresAt <= now.Unix() || p.ExpiresAt <= p.CreatedAt || p.ExpiresAt-p.CreatedAt > 3600 {
		return errors.New("endpoint update expired or has invalid timestamps")
	}
	return nil
}
