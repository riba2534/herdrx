package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func TestReusableKeysAndFoldersHTTP(t *testing.T) {
	a, db, srv, admin, info := authFixture(t)
	csrf := info["csrf_token"].(string)
	owner := info["user"].(map[string]any)["id"].(string)
	url := srv.URL
	anonymous := newTestClient(t)
	requestJSON(t, anonymous, "GET", url+"/api/ssh-keys/", "", nil, 401)
	requestJSON(t, admin, "POST", url+"/api/ssh-keys/", "wrong", map[string]any{"name": "Shared", "generate": true}, 403)
	private, public, err := secure.GenerateSSHKey("fixture")
	if err != nil {
		t.Fatal(err)
	}
	key := postJSON(t, admin, url+"/api/ssh-keys/", csrf, map[string]any{"name": "Shared", "private_key": string(private), "public_key": string(public)}, 201)["key"].(map[string]any)
	kid := key["id"].(string)
	encoded, _ := json.Marshal(key)
	if bytes.Contains(encoded, private) || strings.Contains(string(encoded), "private_key") || strings.Contains(string(encoded), "passphrase") {
		t.Fatal("key response exposed secret material")
	}
	c, err := db.CredentialByID(context.Background(), owner, kid)
	if err != nil || bytes.Contains(c.Ciphertext, private) {
		t.Fatal("key is not encrypted")
	}
	if _, err = hostruntime.OpenCredential(a.vault, "another-owner", kid, c.Kind, c.Ciphertext); err == nil {
		t.Fatal("key encryption is not bound to owner")
	}
	for _, input := range []map[string]any{
		{"name": "Invalid", "private_key": string(public)},
		{"name": "Invalid", "private_key": string(private), "public_key": "invalid"},
		{"name": "Invalid", "private_key": string(private), "certificate": string(public)},
		{"name": "Invalid\nname", "generate": true},
	} {
		postJSON(t, admin, url+"/api/ssh-keys/", csrf, input, 400)
	}
	requestJSON(t, admin, "PATCH", url+"/api/ssh-keys/"+kid, csrf, map[string]any{"name": "Renamed", "revision": 1}, 200)
	requestJSON(t, admin, "PATCH", url+"/api/ssh-keys/"+kid, csrf, map[string]any{"name": "Stale", "revision": 1}, 409)
	createFolder := func(name, parent string) string {
		return postJSON(t, admin, url+"/api/host-folders/", csrf, map[string]any{"name": name, "parent_id": parent}, 201)["folder"].(map[string]any)["id"].(string)
	}
	root := createFolder("Production", "")
	child := createFolder("Applications", root)
	grand := createFolder("Workers", child)
	requestJSON(t, admin, "PATCH", url+"/api/host-folders/"+root, csrf, map[string]any{"name": "Production", "parent_id": grand}, 400)
	hostBody := func(name string) map[string]any {
		return map[string]any{"name": name, "transport": "ssh", "hostname": "example.test", "username": "deploy", "port": 2222, "auth_method": "saved_key", "ssh_key_id": kid, "folder_id": child}
	}
	hosts := []string{}
	for _, name := range []string{"One", "Two"} {
		h := postJSON(t, admin, url+"/api/hosts/", csrf, hostBody(name), 201)["host"].(map[string]any)
		if h["ssh_key_id"] != kid || h["folder_id"] != child || h["username"] != "deploy" || h["port"] != float64(2222) {
			t.Fatalf("wrong SSH host: %v", h)
		}
		hosts = append(hosts, h["id"].(string))
	}
	k, err := db.SSHKeyByID(context.Background(), owner, kid)
	if err != nil || k.HostCount != 2 {
		t.Fatal("shared key references not counted")
	}
	requestJSON(t, admin, "DELETE", url+"/api/ssh-keys/"+kid, csrf, nil, 409)
	requestJSON(t, admin, "DELETE", url+"/api/hosts/"+hosts[0]+"/", csrf, nil, 200)
	if _, err = db.CredentialByID(context.Background(), owner, kid); err != nil {
		t.Fatal("deleting a host deleted a reusable key")
	}
	invite := postJSON(t, admin, url+"/api/admin/invites", csrf, nil, 201)
	member := newTestClient(t)
	registration := postJSON(t, member, url+"/api/register", "", map[string]any{"email": "member@example.test", "display_name": "Member", "password": "member-fixture-password", "invite_code": invite["code"]}, 200)
	memberCSRF := registration["csrf_token"].(string)
	for _, resource := range []string{"ssh-keys", "host-folders"} {
		listed := requestJSON(t, member, "GET", url+"/api/"+resource+"/", "", nil, 200)
		field := "keys"
		if resource == "host-folders" {
			field = "folders"
		}
		if len(listed[field].([]any)) != 0 {
			t.Fatal("other tenant listed resources")
		}
	}
	requestJSON(t, member, "PATCH", url+"/api/ssh-keys/"+kid, memberCSRF, map[string]any{"name": "Intruder", "revision": 2}, 404)
	requestJSON(t, member, "DELETE", url+"/api/ssh-keys/"+kid, memberCSRF, nil, 404)
	requestJSON(t, member, "DELETE", url+"/api/host-folders/"+child, memberCSRF, nil, 404)
	requestJSON(t, member, "PATCH", url+"/api/hosts/"+hosts[1]+"/folder", memberCSRF, map[string]any{"folder_id": ""}, 404)
	postJSON(t, member, url+"/api/hosts/", memberCSRF, hostBody("Intruder"), 400)
	postJSON(t, member, url+"/api/host-folders/", memberCSRF, map[string]any{"name": "Intruder", "parent_id": root}, 404)
	requestJSON(t, admin, "DELETE", url+"/api/host-folders/"+child, csrf, nil, 200)
	h, err := db.HostByID(context.Background(), owner, hosts[1])
	if err != nil || h.FolderID != root || h.CredentialID != kid {
		t.Fatal("folder deletion lost host or changed credential")
	}
	folders, _ := db.ListHostFolders(context.Background(), owner)
	for _, f := range folders {
		if f.ID == grand && f.ParentID != root {
			t.Fatal("subfolder was not moved to parent")
		}
	}
	requestJSON(t, admin, "PATCH", url+"/api/hosts/"+hosts[1]+"/folder", csrf, map[string]any{"folder_id": ""}, 200)
	body := hostBody("Two")
	body["auth_method"] = "password"
	body["secret"] = "only-test-password"
	body["folder_id"] = ""
	body["username"] = "custom-user"
	body["port"] = 2200
	updated := requestJSON(t, admin, "PUT", url+"/api/hosts/"+hosts[1]+"/ssh", csrf, body, 200)["host"].(map[string]any)
	if updated["username"] != "custom-user" || updated["port"] != float64(2200) {
		t.Fatal("custom SSH settings were lost")
	}
	delete(body, "secret")
	body["keep_secret"] = true
	body["port"] = 2201
	requestJSON(t, admin, "PUT", url+"/api/hosts/"+hosts[1]+"/ssh", csrf, body, 200)
	requestJSON(t, admin, "DELETE", url+"/api/ssh-keys/"+kid, csrf, nil, 200)
	if _, err = db.CredentialByID(context.Background(), owner, kid); err != store.ErrNotFound {
		t.Fatal("unused key credential not deleted")
	}
	events, err := db.ListAudit(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "only-test-password") || bytes.Contains(raw, private) {
		t.Fatal("audit exposed secrets")
	}
}
