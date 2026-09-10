package store

import (
	"context"
	"errors"
	"testing"
)

func TestHostQueriesAreTenantScoped(t *testing.T) {
	dataStore, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dataStore.Close()
	ctx := context.Background()
	for _, user := range []User{
		{ID: "usr_a", Email: "a@example.test", PasswordHash: "hash", DisplayName: "A", Role: "admin"},
		{ID: "usr_b", Email: "b@example.test", PasswordHash: "hash", DisplayName: "B", Role: "user"},
	} {
		if err := dataStore.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if err := dataStore.CreateHost(ctx, Host{ID: "hst_a", OwnerID: "usr_a", Name: "A", Transport: "local", Port: 22}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataStore.HostByID(ctx, "usr_b", "hst_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant accessed host: %v", err)
	}
	hosts, err := dataStore.ListHosts(ctx, "usr_b")
	if err != nil || len(hosts) != 0 {
		t.Fatalf("other tenant listed hosts: %#v, %v", hosts, err)
	}
	for _, transport := range []string{"local", "ssh", "tailcat"} {
		id := "hst_" + transport
		original := Host{ID: id, OwnerID: "usr_a", Name: "Original", Transport: transport, Hostname: "example.test", Port: 2222, Username: "tester", SessionName: "work", HostKey: "known-host-key", TailcatAddr: "tailcat-address"}
		if transport == "ssh" {
			original.ProxyJump = "jump@bastion.example:22"
		}
		if err := dataStore.CreateHost(ctx, original); err != nil {
			t.Fatal(err)
		}
		before, _ := dataStore.HostByID(ctx, "usr_a", id)
		if transport == "ssh" && before.ProxyJump != "jump@bastion.example:22" {
			t.Fatalf("proxy_jump not persisted: %+v", before)
		}
		if _, err := dataStore.RenameHost(ctx, "usr_b", id, "Intruder"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other tenant renamed host: %v", err)
		}
		renamed, err := dataStore.RenameHost(ctx, "usr_a", id, "新名称")
		if err != nil || renamed.Name != "新名称" {
			t.Fatalf("rename failed: %v", err)
		}
		before.Name = renamed.Name
		before.UpdatedAt = renamed.UpdatedAt
		if before != renamed {
			t.Fatalf("renaming changed connection settings: before=%+v after=%+v", before, renamed)
		}
		persisted, _ := dataStore.HostByID(ctx, "usr_a", id)
		if persisted.Name != renamed.Name {
			t.Fatal("new name was not persisted")
		}
	}
}
