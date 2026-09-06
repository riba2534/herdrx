package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/riba2534/herdrx/internal/store"
)

func testSubscription(t *testing.T, endpoint string) store.PushSubscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return store.PushSubscription{Endpoint: endpoint, P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}
}

func TestNotifyRejectsUnsafeDestinationBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(201) }))
	defer server.Close()
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, endpoint := range []string{server.URL, "http://push.example.test/send", "https://127.0.0.1/send", "https://169.254.169.254/latest", "https://10.1.2.3/send", "https://[::1]/send", "https://[::ffff:127.0.0.1]/send", "https://100.100.100.200/send", "https://push.example.test:8443/send", "https://user:password@push.example.test/send", "https://push.example.test/send#fragment"} {
		if _, err := service.Notify(context.Background(), testSubscription(t, endpoint), Notification{Title: "fixture"}); err == nil {
			t.Fatalf("unsafe destination accepted: %s", endpoint)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("unsafe notification reached the network")
	}
}

func TestPushDialPinsDNSAndRejectsEveryPrivateAnswer(t *testing.T) {
	for _, addresses := range [][]string{{"127.0.0.1"}, {"8.8.8.8", "10.0.0.1"}, {"::1"}, {"100.64.0.1"}, {"198.18.0.1"}, {"64:ff9b::7f00:1"}, {"64:ff9b:1::1"}, {"8.8.8.8", "2606:4700:4700::1111"}} {
		lookups, calls := 0, []string{}
		d := pushDialer{
			lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				lookups++
				if lookups != 1 || host != "push.example.test" {
					t.Fatal("hostname resolved again after validation")
				}
				var result []netip.Addr
				for _, value := range addresses {
					result = append(result, netip.MustParseAddr(value))
				}
				return result, nil
			},
			dial: func(_ context.Context, _ string, address string) (net.Conn, error) {
				calls = append(calls, address)
				return nil, errors.New("fixture dial failure")
			},
		}
		_, err := d.DialContext(context.Background(), "tcp", "push.example.test:443")
		if err == nil {
			t.Fatal("dial unexpectedly succeeded")
		}
		if addresses[0] == "8.8.8.8" && len(addresses) == 2 && strings.HasPrefix(addresses[1], "2606:") {
			if strings.Join(calls, ",") != "8.8.8.8:443,[2606:4700:4700::1111]:443" {
				t.Fatal("did not dial checked literals", calls)
			}
		} else if len(calls) != 0 {
			t.Fatal("dialed a DNS set containing an unsafe address", addresses, calls)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPushRedirectCannotMakeSecondRequest(t *testing.T) {
	client := newHTTPClient()
	calls := 0
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": {"https://169.254.169.254/"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	service, err := OpenWithClient(t.TempDir(), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Notify(context.Background(), testSubscription(t, "https://push.example.test/send"), Notification{Title: "fixture"}); err == nil {
		t.Fatal("redirect treated as delivered")
	}
	if calls != 1 {
		t.Fatal("followed untrusted redirect", calls)
	}
}

func TestSubscriptionKeysAndProxyPolicy(t *testing.T) {
	subscription := testSubscription(t, "https://push.example.test/send")
	if err := ValidateSubscription(subscription.Endpoint, subscription.P256DH, subscription.Auth); err != nil {
		t.Fatal(err)
	}
	for _, value := range [][2]string{{"bad", subscription.Auth}, {subscription.P256DH, "bad"}, {base64.RawURLEncoding.EncodeToString(make([]byte, 65)), subscription.Auth}} {
		if ValidateSubscription(subscription.Endpoint, value[0], value[1]) == nil {
			t.Fatal("invalid browser keys accepted")
		}
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1234")
	client := newHTTPClient()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.DialContext == nil || client.Timeout == 0 {
		t.Fatal("push transport lost outbound policy")
	}
}

func TestPushErrorDoesNotExposeSubscriptionURL(t *testing.T) {
	client := newHTTPClient()
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	service, err := OpenWithClient(t.TempDir(), client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Notify(context.Background(), testSubscription(t, "https://push.example.test/private-subscription-token"), Notification{Title: "fixture"})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "private-subscription-token") {
		t.Fatal("delivery error exposed the subscription or lost cancellation", err)
	}
}
