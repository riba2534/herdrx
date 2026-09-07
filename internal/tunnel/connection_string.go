package tunnel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tailscale/tailcat"
	"golang.org/x/crypto/ssh"
	"tailscale.com/types/key"
)

const (
	PrefixV1               = "herdrx://v1/"
	MaxConnectionStringLen = 16 * 1024 // 16 KiB
)

type ConnectionPayload struct {
	RelayProbeNode string `json:"relay_probe_node,omitempty"`
	V              int    `json:"v"`
	AgentID        string `json:"agent_id"`
	TailcatAddr    string `json:"tc"`
	ClientPriv     string `json:"tc_client_priv"`
	SSHHostKey     string `json:"ssh_host_key"`
	EnrollmentID   string `json:"enrollment_id"`
	PairSecret     string `json:"pair_secret"`
	Host           string `json:"host,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	AgentVer       string `json:"agent_ver,omitempty"`
	Exp            int64  `json:"exp,omitempty"`
}

type ParsedConnection struct {
	Payload    ConnectionPayload
	ClientNode key.NodePrivate
	SSHHostKey ssh.PublicKey
}

// BuildConnectionString 构造标准 herdrx://v1/<base64url(JSON)> 连接串
func BuildConnectionString(p ConnectionPayload) (string, error) {
	if p.V != 1 {
		p.V = 1
	}
	if p.AgentID == "" || p.TailcatAddr == "" || p.ClientPriv == "" || p.SSHHostKey == "" || p.EnrollmentID == "" || p.PairSecret == "" {
		return "", errors.New("missing required payload fields")
	}

	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(data)
	fullStr := PrefixV1 + encoded
	if len(fullStr) > MaxConnectionStringLen {
		return "", fmt.Errorf("connection string exceeds maximum limit of %d bytes", MaxConnectionStringLen)
	}
	return fullStr, nil
}

// ParseConnectionString 严格解析连接串，执行 16KiB 长度、JSON 语法、未知字段、公钥与私钥合法性校验
func ParseConnectionString(raw string) (*ParsedConnection, error) {
	rawTrimmed := strings.TrimSpace(raw)
	if len(rawTrimmed) > MaxConnectionStringLen {
		return nil, fmt.Errorf("connection string exceeds maximum limit of %d bytes", MaxConnectionStringLen)
	}
	if !strings.HasPrefix(rawTrimmed, PrefixV1) {
		if strings.HasPrefix(rawTrimmed, "herdrx://") {
			return nil, fmt.Errorf("unsupported protocol version: expected %s", PrefixV1)
		}
		if strings.Contains(rawTrimmed, "/#pair=") {
			return nil, errors.New("legacy #pair= URL is deprecated; please run 'herdrx connect' on server to get a herdrx://v1/ connection string")
		}
		return nil, fmt.Errorf("invalid connection string prefix; must start with %s", PrefixV1)
	}

	encodedPart := strings.TrimPrefix(rawTrimmed, PrefixV1)
	decodedBytes, err := base64.RawURLEncoding.DecodeString(encodedPart)
	if err != nil {
		return nil, fmt.Errorf("decode base64url payload: %w", err)
	}

	// 严格模式解码 JSON，拦截未知字段
	decoder := json.NewDecoder(bytes.NewReader(decodedBytes))
	decoder.DisallowUnknownFields()

	var payload ConnectionPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid json payload: %w", err)
	}

	if payload.V != 1 {
		return nil, fmt.Errorf("unsupported payload version %d", payload.V)
	}
	if payload.RelayProbeNode != "" {
		var probe key.NodePublic
		if probe.UnmarshalText([]byte(payload.RelayProbeNode)) != nil || probe.IsZero() {
			return nil, errors.New("invalid relay probe identity")
		}
	}
	if payload.AgentID == "" || len(payload.AgentID) > 128 {
		return nil, errors.New("invalid or empty agent_id")
	}
	if payload.EnrollmentID == "" || len(payload.EnrollmentID) < 8 || len(payload.EnrollmentID) > 128 {
		return nil, errors.New("invalid or empty enrollment_id")
	}
	if len(payload.PairSecret) < 16 {
		return nil, errors.New("pair_secret is too short (must be >= 16 chars)")
	}

	// 校验 Tailcat 临时端点地址：调用上游官方 ParseAddr 解析并验证非零 PSK，坚决杜绝无 PSK 降级
	ci, err := tailcat.ParseAddr(tailcat.Addr(payload.TailcatAddr))
	if err != nil {
		return nil, fmt.Errorf("invalid tailcat address: %w", err)
	}
	if ci.PresharedKey.IsZero() {
		return nil, errors.New("tailcat address must contain PSK parameter; insecure no-PSK downgrade is strictly prohibited")
	}

	// 解析客户端临时私钥
	var clientPriv key.NodePrivate
	if err := clientPriv.UnmarshalText([]byte(payload.ClientPriv)); err != nil || clientPriv.IsZero() {
		return nil, fmt.Errorf("invalid tc_client_priv: %w", err)
	}

	// 解析服务端固定 SSH 主机公钥
	sshPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.SSHHostKey))
	if err != nil {
		return nil, fmt.Errorf("invalid ssh_host_key: %w", err)
	}

	return &ParsedConnection{
		Payload:    payload,
		ClientNode: clientPriv,
		SSHHostKey: sshPub,
	}, nil
}
