package agentcli

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunBoundedCommand_Timeout(t *testing.T) {
	// 执行 sleep 10s 命令，超时设为 100ms
	ctx := context.Background()
	_, err := RunBoundedCommand(ctx, 100*time.Millisecond, 1024, "sleep", "10")
	if err == nil {
		t.Fatalf("expected timeout error for hanging command, but got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timed out error, got: %v", err)
	}
}

func TestRunBoundedCommand_OutputLimit(t *testing.T) {
	// 输出超过 100 字节的数据流，限额设为 50 字节
	ctx := context.Background()
	_, err := RunBoundedCommand(ctx, 2*time.Second, 50, "sh", "-c", "printf '%0.sA' $(seq 1 200)")
	if err == nil {
		t.Fatalf("expected output limit exceeded error, but got nil")
	}
	if !strings.Contains(err.Error(), "exceeded maximum limit") {
		t.Fatalf("expected limit exceeded error, got: %v", err)
	}
}

func TestRunBoundedCommand_Success(t *testing.T) {
	ctx := context.Background()
	out, err := RunBoundedCommand(ctx, 2*time.Second, 1024, "sh", "-c", "printf 'hello bounded'")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if string(out) != "hello bounded" {
		t.Fatalf("unexpected output: %s", string(out))
	}
}
