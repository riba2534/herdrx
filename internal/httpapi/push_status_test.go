package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/riba2534/herdrx/internal/store"
)

func TestPushSubscriptionStatusOnlyConfirmsTheSignedInUsersLiveRecord(t *testing.T) {
	_, db, srv, admin, info := authFixture(t)
	ctx := context.Background()
	csrf := info["csrf_token"].(string)
	adminID := info["user"].(map[string]any)["id"].(string)
	invite := postJSON(t, admin, srv.URL+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	registered := postJSON(t, member, srv.URL+"/api/register", "", map[string]any{"email": "status@example.test", "display_name": "Member", "password": "fixture-member-password", "invite_code": invite["code"]}, 200)
	memberCSRF := registered["csrf_token"].(string)
	const endpoint = "https://push.example.test/status-admin"
	if err := db.UpsertPushSubscription(ctx, store.PushSubscription{ID: "push-status", UserID: adminID, Endpoint: endpoint, P256DH: "unused", Auth: "unused"}); err != nil {
		t.Fatal(err)
	}
	status := func(client *http.Client, token, value string) any {
		return requestJSON(t, client, "POST", srv.URL+"/api/push/subscriptions/status", token, map[string]string{"endpoint": value}, 200)["subscribed"]
	}
	if status(admin, csrf, endpoint) != true {
		t.Fatal("the owner's live subscription was not confirmed")
	}
	// Another user in the same browser profile must not inherit the subscription.
	if status(member, memberCSRF, endpoint) != false {
		t.Fatal("another user's subscription was reported as this user's")
	}
	if status(admin, csrf, "https://push.example.test/unknown") != false {
		t.Fatal("an unknown endpoint was reported as subscribed")
	}
	// A record removed by the server (expiry, account disable) reads as unsubscribed.
	current, err := db.PushSubscriptionByEndpoint(ctx, adminID, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePushSubscriptionIfCurrent(ctx, current); err != nil {
		t.Fatal(err)
	}
	if status(admin, csrf, endpoint) != false {
		t.Fatal("a deleted subscription was reported as subscribed")
	}
	requestJSON(t, admin, "POST", srv.URL+"/api/push/subscriptions/status", csrf, map[string]string{"endpoint": ""}, 400)
	requestJSON(t, admin, "POST", srv.URL+"/api/push/subscriptions/status", "", map[string]string{"endpoint": endpoint}, 403)
}
