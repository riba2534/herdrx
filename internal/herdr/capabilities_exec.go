package herdr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

const capabilityOutputLimit = 2 << 20
const capabilityCommandTimeout = 5 * time.Second

func readCLIIdentity(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) (CLIIdentity, error) {
	version, err := run(ctx, "--version")
	if err != nil {
		return CLIIdentity{}, err
	}
	schema, err := run(ctx, "api", "schema", "--json")
	if err != nil {
		return CLIIdentity{}, err
	}
	return ParseCLIIdentity(version, schema)
}

func (e *LocalEndpoint) HerdrCLIIdentity(ctx context.Context) (CLIIdentity, error) {
	return readCLIIdentity(ctx, func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, capabilityCommandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, e.Binary, args...)
		cmd.WaitDelay = time.Second
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		// A wrapper's descendant can inherit stdout after the wrapper is killed.
		// Closing our read end also bounds that case; WaitDelay alone starts too late.
		stopRead := context.AfterFunc(ctx, func() { _ = stdout.Close() })
		defer stopRead()
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("执行 Herdr 只读诊断失败: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(stdout, capabilityOutputLimit+1))
		if readErr != nil || len(data) > capabilityOutputLimit {
			cancel()
		}
		waitErr := cmd.Wait()
		if len(data) > capabilityOutputLimit {
			return nil, errors.New("Herdr 诊断响应超过大小限制")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, readErr
		}
		if waitErr != nil {
			return nil, fmt.Errorf("执行 Herdr 只读诊断失败: %w", waitErr)
		}
		return data, nil
	})
}

func (e *SSHEndpoint) HerdrCLIIdentity(ctx context.Context) (CLIIdentity, error) {
	return readCLIIdentity(ctx, func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, capabilityCommandTimeout)
		defer cancel()
		session, err := e.newSession(ctx)
		if err != nil {
			return nil, err
		}
		defer session.Close()
		stop := context.AfterFunc(ctx, func() { _ = session.Close() })
		defer stop()
		stdout, err := session.StdoutPipe()
		if err != nil {
			return nil, err
		}
		// Only fixed, read-only command tokens reach this path. Tailcat parses
		// the same two commands with an exact allowlist and no remote shell.
		command := terminalCommand(e.host.Transport, append([]string{"herdr"}, args...))
		if err := session.Start(command); err != nil {
			return nil, fmt.Errorf("启动 Herdr 只读诊断失败: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(stdout, capabilityOutputLimit+1))
		if readErr != nil || len(data) > capabilityOutputLimit {
			_ = session.Close()
		}
		waitErr := session.Wait()
		if len(data) > capabilityOutputLimit {
			return nil, errors.New("Herdr 诊断响应超过大小限制")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr != nil {
			return nil, readErr
		}
		if waitErr != nil {
			return nil, fmt.Errorf("执行 Herdr 只读诊断失败: %w", waitErr)
		}
		return data, nil
	})
}

func (e *LocalEndpoint) HerdrCapabilities(ctx context.Context) (CapabilityReport, error) {
	return ProbeCapabilities(ctx, e)
}

func (e *SSHEndpoint) HerdrCapabilities(ctx context.Context) (CapabilityReport, error) {
	return ProbeCapabilities(ctx, e)
}
