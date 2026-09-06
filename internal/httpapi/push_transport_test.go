package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/riba2534/herdrx/internal/push"
)

// Only test code maps the reserved provider hostname to a local HTTP fixture.
// The production client always checks public DNS and verifies HTTPS.
type fixturePushClient struct{ server *httptest.Server }

func (c fixturePushClient) Do(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	target, err := url.Parse(c.server.URL)
	if err != nil {
		return nil, err
	}
	copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
	return c.server.Client().Do(copy)
}

func setFixturePushClient(t *testing.T, api *API, provider *httptest.Server) {
	t.Helper()
	api.stopBackground()
	api.push.Close()
	service, err := push.OpenWithClient(api.config.DataDir, fixturePushClient{server: provider})
	if err != nil {
		t.Fatal(err)
	}
	api.push = service
}

func TestPushSubscriptionRejectsUnsafeInputBeforePersistence(t *testing.T) {
	_, db, server, client, info := authFixture(t)
	for _, endpoint := range []string{"http://127.0.0.1/", "https://169.254.169.254/", "https://user:pass@push.example.test/", "https://push.example.test/"} {
		requestJSON(t, client, "POST", server.URL+"/api/push/subscriptions", info["csrf_token"].(string), map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": "invalid", "auth": "invalid"}}, 400)
	}
	subscriptions, err := db.PushSubscriptions(context.Background())
	if err != nil || len(subscriptions) != 0 {
		t.Fatal("invalid subscription persisted", err)
	}
}
