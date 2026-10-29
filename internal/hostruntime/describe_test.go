package hostruntime

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/herdr"
	"github.com/riba2534/herdrx/internal/store"
)

func TestDescribeErrorExplainsOfflineCauses(t *testing.T) {
	for _, tc := range []struct {
		name, code, hint string
		err              error
	}{
		{"tailcat-socket", "herdr_not_running", "启动 Herdr", errors.New("forward remote herdr socket /home/u/.config/herdr/herdr.sock: ssh: rejected: connect failed (Herdr socket is unavailable)")},
		{"local-socket", "herdr_not_running", "会话名", errors.New("connect herdr API socket /tmp/x/herdr.sock: dial unix /tmp/x/herdr.sock: connect: no such file or directory")},
		{"openssh-denied", "host_auth_failed", "Kerberos", herdr.WithStderrTail(errors.New("system OpenSSH 连接失败"), "SSH 输出", "debug1: Next authentication method\nuser@example.test: Permission denied (gssapi-with-mic,publickey).\n")},
		{"known-hosts", "host_key_unverified", "known_hosts", herdr.WithStderrTail(errors.New("system OpenSSH 连接失败"), "SSH 输出", "Host key verification failed.\n")},
		{"dns", "host_unreachable", "DNS", errors.New("dial SSH missing.example.test:22: dial tcp: lookup missing.example.test: no such host")},
		{"refused", "host_unreachable", "SSH 服务", errors.New("dial SSH 192.0.2.1:22: dial tcp 192.0.2.1:22: connect: connection refused")},
		{"other", "host_unavailable", "", errors.New("something else")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := DescribeError(tc.err)
			if detail.Code != tc.code || detail.Permanent {
				t.Fatalf("code=%s permanent=%v: %v", detail.Code, detail.Permanent, detail)
			}
			if !strings.Contains(detail.Error(), tc.hint) || !strings.Contains(detail.Error(), tc.err.Error()) {
				t.Fatalf("message lacks next step or cause: %v", detail)
			}
		})
	}
	// A changed key must never read as a missing fingerprint to add.
	changed := DescribeError(herdr.WithStderrTail(errors.New("system OpenSSH 连接失败"), "SSH 输出", "@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@\nHost key for remote.example.test has changed and you have requested strict checking.\nHost key verification failed.\n"))
	if changed.Code != "host_key_changed" || !changed.Permanent || !strings.Contains(changed.Error(), "中间人攻击") || !strings.Contains(changed.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(changed.Error(), "添加") {
		t.Fatalf("changed host key: code=%s permanent=%v %v", changed.Code, changed.Permanent, changed)
	}
	// The SSH tail line stays visible to the user.
	detail := DescribeError(herdr.WithStderrTail(errors.New("system OpenSSH 连接失败"), "SSH 输出", "Host key verification failed.\n"))
	if !strings.Contains(detail.Error(), "SSH 输出：Host key verification failed.") {
		t.Fatalf("tail missing: %v", detail)
	}
}

type eventTestEndpoint struct {
	testEndpoint
	err error
}

func (e *eventTestEndpoint) SubscribeEvents(_ context.Context, _ []herdr.EventSubscription, onEvent func(herdr.Event)) error {
	onEvent(herdr.Event{Event: herdr.EventSubscriptionStarted})
	return e.err
}

func TestSharedHandleForwardsEventsWithoutInvalidating(t *testing.T) {
	streaming := &eventTestEndpoint{err: io.EOF}
	f := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) { return streaming, nil }}
	defer f.Close()
	handle, err := f.Open(context.Background(), store.Host{ID: "events", Transport: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	started := false
	err = handle.(herdr.EventEndpoint).SubscribeEvents(context.Background(), nil, func(event herdr.Event) { started = event.Event == herdr.EventSubscriptionStarted })
	if !errors.Is(err, io.EOF) || !started {
		t.Fatalf("err=%v started=%v", err, started)
	}
	// A stream Herdr ended is not a dead transport: the cached endpoint stays.
	if streaming.closes.Load() != 0 {
		t.Fatal("ending an event stream invalidated the shared transport")
	}

	plain := new(testEndpoint)
	g := &Factory{dial: func(context.Context, store.Host) (herdr.Endpoint, error) { return plain, nil }}
	defer g.Close()
	other, err := g.Open(context.Background(), store.Host{ID: "plain", Transport: "ssh"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.(herdr.EventEndpoint).SubscribeEvents(context.Background(), nil, func(herdr.Event) {}); !errors.Is(err, herdr.ErrEventsUnsupported) {
		t.Fatalf("unsupported transport err = %v", err)
	}
}
