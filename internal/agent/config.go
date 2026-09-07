package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

type EnrollmentConfig struct {
	EnrollmentID      string               `json:"enrollment_id,omitempty"`
	EphemeralNodeKey  key.NodePrivate      `json:"ephemeral_node_key,omitempty"`
	EphemeralPSK      tailcat.PresharedKey `json:"ephemeral_psk,omitempty"`
	ClientPrivate     key.NodePrivate      `json:"client_private,omitempty"`
	ConnectionString  string               `json:"connection_string,omitempty"`
	AllowedClientNode string               `json:"allowed_client_node,omitempty"`
	PairSecret        string               `json:"pair_secret,omitempty"`
	ExpiresAt         time.Time            `json:"expires_at,omitempty"`
}

type BindingConfig struct {
	Status             string    `json:"status,omitempty"` // none, prepared, active, revoked
	BindingID          string    `json:"binding_id,omitempty"`
	EnrollmentID       string    `json:"enrollment_id,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	ControllerID       string    `json:"controller_id,omitempty"`
	FormalClientNode   string    `json:"formal_client_node,omitempty"`
	FormalSSHPublicKey string    `json:"formal_ssh_public_key,omitempty"`
	FormalTailcatAddr  string    `json:"formal_tailcat_addr,omitempty"`
	Challenge          string    `json:"challenge,omitempty"`
	PreparedExpiresAt  time.Time `json:"prepared_expires_at,omitempty"`
	Epoch              int64     `json:"epoch,omitempty"`
}

type Config struct {
	RelayProbePrivate key.NodePrivate      `json:"relay_probe_private,omitempty"`
	EndpointVersion   int64                `json:"endpoint_version,omitempty"`
	Version           int                  `json:"version"`
	Node              tailcat.PrivateKey   `json:"node"`
	PresharedKey      tailcat.PresharedKey `json:"preshared_key,omitempty"`
	AllowedNodeKey    string               `json:"allowed_node_key,omitempty"`
	AuthorizedSSHKey  string               `json:"authorized_ssh_key,omitempty"`
	SSHHostPrivate    string               `json:"ssh_host_private"`
	SSHHostPublic     string               `json:"ssh_host_public,omitempty"`
	PublicURL         string               `json:"public_url,omitempty"`
	SetupID           string               `json:"setup_id,omitempty"`
	PairTokenHash     string               `json:"pair_token_hash,omitempty"`
	PairExpiresAt     time.Time            `json:"pair_expires_at,omitempty"`
	Paired            bool                 `json:"paired"`
	Revoked           bool                 `json:"revoked"`
	HerdrBin          string               `json:"herdr_bin,omitempty"`
	LegacyNoPSK       bool                 `json:"legacy_no_psk,omitempty"`

	// P2 扩展结构：
	Enrollment EnrollmentConfig `json:"enrollment,omitempty"`
	Binding    BindingConfig    `json:"binding,omitempty"`
}

// FormalPSK 获取正式通信所需的 WireGuard 预共享密钥，消除顶层与 Node.Public 双来源歧义
func (c *Config) FormalPSK() tailcat.PresharedKey {
	if !c.PresharedKey.IsZero() {
		return c.PresharedKey
	}
	if !c.Node.Public.PresharedKey.IsZero() {
		return c.Node.Public.PresharedKey
	}
	return tailcat.PresharedKey{}
}

// SyncPSK 保证顶层 PresharedKey 与 Node.Public.PresharedKey 互相同步对齐，避免零值交由 Tailcat 丢弃
func (c *Config) SyncPSK() {
	if c.PresharedKey.IsZero() && !c.Node.Public.PresharedKey.IsZero() {
		c.PresharedKey = c.Node.Public.PresharedKey
	} else if !c.PresharedKey.IsZero() && c.Node.Public.PresharedKey.IsZero() {
		c.Node.Public.PresharedKey = c.PresharedKey
	}
}

func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "herdrx", "config.json"), nil
}

func Load(path string) (Config, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agent config: %w", err)
	}
	return Decode(encoded)
}

// Decode validates a config document already read from a trusted descriptor.
func Decode(encoded []byte) (Config, error) {
	var config Config
	if err := json.Unmarshal(encoded, &config); err != nil {
		return Config{}, fmt.Errorf("decode agent config: %w", err)
	}
	if config.EndpointVersion < 0 || config.Version != 1 || config.Node.Private.IsZero() || config.SSHHostPrivate == "" {
		return Config{}, fmt.Errorf("agent config is incomplete")
	}
	// 若标记为 Paired 且非 P2 binding 模式，严格要求 legacy 对端白名单与公钥
	if config.Paired && config.Binding.Status == "" && (config.AllowedNodeKey == "" || config.AuthorizedSSHKey == "") {
		return Config{}, fmt.Errorf("paired agent config requires allowed_node_key and authorized_ssh_key")
	}
	if config.Binding.Status == "active" && (config.Binding.FormalClientNode == "" || config.Binding.FormalSSHPublicKey == "") {
		return Config{}, fmt.Errorf("active binding requires formal_client_node and formal_ssh_public_key")
	}
	config.SyncPSK()
	return config, nil
}

func Save(path string, config Config) error {
	config.SyncPSK()
	dirPath := filepath.Dir(path)
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		return fmt.Errorf("create agent config directory: %w", err)
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dirPath, ".config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}

	// 同步父目录确保元数据落盘
	dir, err := os.Open(dirPath)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	return nil
}

// MaskAddress 安全脱敏 Tailcat 地址（遮蔽 PSK），防止密钥暴露
func MaskAddress(addr string) string {
	if len(addr) <= 24 {
		return "***"
	}
	parts := strings.Split(addr, "?")
	if len(parts) == 1 {
		if len(addr) > 20 {
			return addr[:12] + "..." + addr[len(addr)-6:]
		}
		return addr[:8] + "..."
	}
	base := parts[0]
	if len(base) > 16 {
		base = base[:10] + "..." + base[len(base)-4:]
	}
	return base + "?psk=[REDACTED]"
}

func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// NextEpoch remains strictly increasing even when the system clock moves back.
func NextEpoch(previous int64) int64 { return max(previous+1, time.Now().UnixNano()) }
