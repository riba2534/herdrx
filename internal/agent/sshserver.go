package agent

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/riba2534/herdrx/internal/herdrpaths"
	"github.com/riba2534/herdrx/internal/secure"
	"golang.org/x/crypto/ssh"
)

type ConfigUpdater interface {
	Update(mutator func(*Config) error) (Config, error)
}

type SSHServer struct {
	configPath string
	config     *ssh.ServerConfig
	updater    ConfigUpdater
}

func (s *SSHServer) SetUpdater(updater ConfigUpdater) {
	s.updater = updater
}

func NewSSHServer(configPath string, agentConfig Config) (*SSHServer, error) {
	authorizedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(agentConfig.AuthorizedSSHKey))
	if err != nil {
		return nil, fmt.Errorf("parse authorized SSH key: %w", err)
	}
	hostKey, err := ssh.ParsePrivateKey([]byte(agentConfig.SSHHostPrivate))
	if err != nil {
		return nil, fmt.Errorf("parse SSH host key: %w", err)
	}
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			latest, err := Load(configPath)
			if err != nil || latest.Revoked {
				return nil, fmt.Errorf("agent access is revoked")
			}
			if !keyEqual(authorizedKey, key) {
				return nil, fmt.Errorf("SSH public key is not authorized")
			}
			role := "unpaired"
			if latest.Paired {
				role = "paired"
			}
			return &ssh.Permissions{
				Extensions: map[string]string{
					"role": role,
				},
			}, nil
		},
		MaxAuthTries: 3,
	}
	serverConfig.AddHostKey(hostKey)
	return &SSHServer{configPath: configPath, config: serverConfig}, nil
}

func (s *SSHServer) Handle(connection net.Conn) {
	defer connection.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(connection, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	done := make(chan struct{})
	defer close(done)
	go s.watchRevocation(done, serverConn)
	go ssh.DiscardRequests(requests)

	// 角色严格取自认证时刻写入 Permissions 的扩展字段，不再猜测认证后磁盘状态
	isPairedAtAuth := (serverConn.Permissions != nil && serverConn.Permissions.Extensions["role"] == "paired")

	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "session":
			go s.handleSession(newChannel, isPairedAtAuth)
		case "direct-streamlocal@openssh.com":
			go s.handleStreamLocal(newChannel, isPairedAtAuth)
		default:
			_ = newChannel.Reject(ssh.UnknownChannelType, "channel type is not allowed")
		}
	}
}

func (s *SSHServer) watchRevocation(done <-chan struct{}, connection *ssh.ServerConn) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			config, err := Load(s.configPath)
			if err != nil || config.Revoked {
				_ = connection.Close()
				return
			}
		}
	}
}

func (s *SSHServer) handleSession(newChannel ssh.NewChannel, isPairedAtAuth bool) {
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if ssh.Unmarshal(request.Payload, &payload) != nil {
			_ = request.Reply(false, nil)
			continue
		}
		spec, err := parseCommand(payload.Command)
		if err != nil {
			_ = request.Reply(false, nil)
			_, _ = io.WriteString(channel.Stderr(), err.Error()+"\n")
			sendExitStatus(channel, 126)
			return
		}
		latest, err := Load(s.configPath)
		if err != nil || latest.Revoked {
			_ = request.Reply(false, nil)
			return
		}
		if (!isPairedAtAuth || !latest.Paired) && spec.Internal != "confirm-pair" {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		status := uint32(0)
		switch spec.Internal {
		case "config-path":
			_, _ = io.WriteString(channel, configPathOutput())
		case "uname":
			_, _ = fmt.Fprintf(channel, "%s %s\n", runtime.GOOS, runtime.GOARCH)
		case "terminal-geometry":
			if err := WriteTerminalGeometry(channel, spec.Args[0]); err != nil {
				_, _ = io.WriteString(channel.Stderr(), err.Error()+"\n")
				status = 1
			}
		case "confirm-pair":
			token := spec.Args[0]
			var pairErr error
			if s.updater != nil {
				_, pairErr = s.updater.Update(func(cfg *Config) error {
					got := base64.RawStdEncoding.EncodeToString(secure.TokenHash(token))
					if cfg.PairTokenHash == "" || len(got) != len(cfg.PairTokenHash) || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.PairTokenHash)) != 1 || time.Now().After(cfg.PairExpiresAt) {
						return fmt.Errorf("pairing token is invalid or expired")
					}
					cfg.PairTokenHash = ""
					cfg.PairExpiresAt = time.Time{}
					cfg.Paired = true
					cfg.Revoked = false
					return nil
				})
			} else {
				pairErr = confirmPair(s.configPath, token)
			}
			if pairErr != nil {
				_, _ = io.WriteString(channel.Stderr(), pairErr.Error()+"\n")
				status = 1
			} else {
				_, _ = io.WriteString(channel, "paired\n")
			}
		case "stage-image":
			ext := spec.Args[0]
			remotePath, err := stageImageFromReader(channel, ext)
			if err != nil {
				_, _ = io.WriteString(channel.Stderr(), err.Error()+"\n")
				status = 1
			} else {
				_, _ = io.WriteString(channel, remotePath+"\n")
			}
		default:
			command := executableCommand(spec, latest.HerdrBin)
			command.Stdin = channel
			command.Stdout = channel
			command.Stderr = channel.Stderr()
			if err := command.Run(); err != nil {
				status = 1
			}
		}
		sendExitStatus(channel, status)
		return
	}
}

func (s *SSHServer) handleStreamLocal(newChannel ssh.NewChannel, isPairedAtAuth bool) {
	if !isPairedAtAuth {
		_ = newChannel.Reject(ssh.Prohibited, "socket forwarding requires an active paired agent")
		return
	}
	latest, err := Load(s.configPath)
	if err != nil || latest.Revoked || !latest.Paired {
		_ = newChannel.Reject(ssh.Prohibited, "socket forwarding requires an active paired agent")
		return
	}
	var payload struct {
		SocketPath string
		Reserved0  string
		Reserved1  uint32
	}
	if ssh.Unmarshal(newChannel.ExtraData(), &payload) != nil || !allowedSocket(payload.SocketPath) {
		_ = newChannel.Reject(ssh.Prohibited, "socket path is not allowed")
		return
	}
	local, err := net.Dial("unix", payload.SocketPath)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "Herdr socket is unavailable")
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = local.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	defer channel.Close()
	defer local.Close()
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(channel, local)
		_ = channel.CloseWrite()
		done <- struct{}{}
	}()
	_, _ = io.Copy(local, channel)
	if closer, ok := local.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	<-done
}

func keyEqual(left, right ssh.PublicKey) bool {
	leftBytes, rightBytes := left.Marshal(), right.Marshal()
	if len(leftBytes) != len(rightBytes) {
		return false
	}
	var difference byte
	for index := range leftBytes {
		difference |= leftBytes[index] ^ rightBytes[index]
	}
	return difference == 0
}

func KeyEqual(left, right ssh.PublicKey) bool {
	return keyEqual(left, right)
}

func StageImageFromReader(r io.Reader, ext string) (string, error) {
	return stageImageFromReader(r, ext)
}

func sendExitStatus(channel ssh.Channel, status uint32) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, status)
	_, _ = channel.SendRequest("exit-status", false, payload)
}

func stagingDir() (string, error) {
	cacheDir, err := herdrpaths.CacheRoot()
	if err != nil || cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "herdrx-staging")
	} else {
		cacheDir = filepath.Join(cacheDir, "herdrx", "staging")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("create staging directory: %w", err)
	}
	return cacheDir, nil
}

func stageImageFromReader(r io.Reader, ext string) (string, error) {
	dir, err := stagingDir()
	if err != nil {
		return "", err
	}
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	filename := fmt.Sprintf("img_%d_%x.%s", time.Now().UnixMilli(), randomBytes, ext)
	filePath := filepath.Join(dir, filename)

	const maxImageSize = 20 << 20 // 20MB
	file, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(r, maxImageSize+1)
	written, err := io.Copy(file, limited)
	if err != nil {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("write staging file: %w", err)
	}
	if written > maxImageSize {
		_ = os.Remove(filePath)
		return "", fmt.Errorf("image exceeds maximum size of 20MB")
	}
	return filePath, nil
}
