package httpapi

import (
	"context"
	"github.com/riba2534/herdrx/internal/store"
	"testing"
	"time"
)

// Measures the additional SQLite admission check under concurrent Web inputs.
// This is a local microbenchmark, not a WAN latency or capacity claim.
func BenchmarkAccessAdmission(b *testing.B) {
	s, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateUser(ctx, store.User{ID: "user", Email: "user@example.test", Role: "user", DisplayName: "User", PasswordHash: "test"}); err != nil {
		b.Fatal(err)
	}
	if err = s.CreateSession(ctx, store.Session{ID: "login", UserID: "user", ExpiresAt: time.Now().Add(time.Hour)}, []byte("test-hash"), "", ""); err != nil {
		b.Fatal(err)
	}
	manager := newAccessManager(s)
	defer manager.close()
	lease, err := manager.attach(ctx, "user", "login")
	if err != nil {
		b.Fatal(err)
	}
	defer lease.release()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := lease.check(); err != nil {
				b.Error(err)
			}
		}
	})
}
