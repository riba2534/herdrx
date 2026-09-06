package tunnel

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riba2534/herdrx/internal/agent"
	"github.com/riba2534/herdrx/internal/secure"
	"tailscale.com/types/key"
)

func enrollmentState(t *testing.T) (*TwoPhaseState, *mockConfigStore, string, string) {
	t.Helper()
	_, pub, err := secure.GenerateSSHKey("state-regression")
	if err != nil {
		t.Fatal(err)
	}
	store := &mockConfigStore{cfg: agent.Config{Enrollment: agent.EnrollmentConfig{
		EnrollmentID: "enrollment-1", ExpiresAt: time.Now().Add(time.Minute),
	}}}
	return NewTwoPhaseState(store), store, key.NewNode().Public().String(), string(pub)
}

func TestPrepareSerializesEndpointClaim(t *testing.T) {
	s, _, node, pub := enrollmentState(t)
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	var starts atomic.Int32
	go func() {
		_, _, _, _, err := s.PrepareWithStarter("enrollment-1", "request-1", "controller-1", node, pub, func() (string, error) {
			starts.Add(1)
			close(entered)
			<-release
			return "formal-original", nil
		})
		firstDone <- err
	}()
	<-entered
	go func() {
		_, _, _, _, err := s.PrepareWithStarter("enrollment-1", "request-2", "controller-2", key.NewNode().Public().String(), pub, func() (string, error) {
			starts.Add(1)
			return "formal-replacement", nil
		})
		secondDone <- err
	}()
	// Keep the first endpoint startup in flight while a competing claim arrives.
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-firstDone; err != nil {
		t.Errorf("original claim failed: %v", err)
	}
	if err := <-secondDone; err == nil {
		t.Error("conflicting claim accepted")
	}
	if got := starts.Load(); got != 1 {
		t.Errorf("started %d endpoints; conflicting claim must not replace the reserved endpoint", got)
	}
	snap, _ := s.Snapshot()
	if snap.FormalTailcatAddr != "formal-original" {
		t.Error("original endpoint was replaced")
	}
}

func TestPrepareRequiresPersistedEnrollment(t *testing.T) {
	for _, name := range []string{"wrong-id", "missing-expiry", "expired", "zero-node", "invalid-ssh"} {
		t.Run(name, func(t *testing.T) {
			s, store, node, pub := enrollmentState(t)
			id := "enrollment-1"
			switch name {
			case "wrong-id":
				id = "another-enrollment"
			case "missing-expiry":
				store.cfg.Enrollment.ExpiresAt = time.Time{}
			case "expired":
				store.cfg.Enrollment.ExpiresAt = time.Now().Add(-time.Second)
			case "zero-node":
				node = key.NodePublic{}.String()
			case "invalid-ssh":
				pub = "not-a-public-key"
			}
			started := false
			_, _, _, _, err := s.PrepareWithStarter(id, "request-1", "controller-1", node, pub, func() (string, error) { started = true; return "endpoint", nil })
			if err == nil || started {
				t.Fatalf("untrusted prepare reached endpoint startup: err=%v started=%v", err, started)
			}
		})
	}
}

func TestPrepareRetryUsesPreparedRecoveryDeadline(t *testing.T) {
	s, store, node, pub := enrollmentState(t)
	binding, challenge, addr, exp, err := s.PrepareWithStarter("enrollment-1", "request-1", "controller-1", node, pub, func() (string, error) { return "endpoint", nil })
	if err != nil {
		t.Fatal(err)
	}
	store.cfg.Enrollment.ExpiresAt = time.Now().Add(-time.Second)
	b2, c2, a2, e2, err := s.PrepareWithStarter("enrollment-1", "request-1", "controller-1", node, pub, func() (string, error) { return "", errors.New("must reuse endpoint") })
	if err != nil || b2 != binding || c2 != challenge || a2 != addr || !e2.Equal(exp) {
		t.Fatalf("same claim did not recover within prepared window: %v", err)
	}
}

func TestRevokeEpochStrictlyIncreases(t *testing.T) {
	s, store, _, _ := enrollmentState(t)
	before := time.Now().Add(time.Hour).UnixNano()
	store.cfg.Binding.Epoch = before
	if err := s.Revoke(); err != nil {
		t.Fatal(err)
	}
	if store.cfg.Binding.Epoch <= before {
		t.Fatal("clock regression reduced authorization epoch")
	}
}

func TestRevokeConcurrent(t *testing.T) {
	s, store, _, _ := enrollmentState(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Revoke()
		}()
	}
	wg.Wait()
	if store.cfg.Binding.Status != "revoked" || !store.cfg.Revoked {
		t.Fatalf("concurrent revoke left status=%s revoked=%v", store.cfg.Binding.Status, store.cfg.Revoked)
	}
	if store.cfg.Binding.Epoch == 0 {
		t.Fatal("concurrent revoke cleared authorization epoch")
	}
}

func TestPreparePersistFailureDoesNotMarkPrepared(t *testing.T) {
	s, store, node, pub := enrollmentState(t)
	store.failNextUpdate = errors.New("injected persist failure")
	started := false
	_, _, _, _, err := s.PrepareWithStarter("enrollment-1", "request-1", "controller-1", node, pub, func() (string, error) {
		started = true
		return "endpoint", nil
	})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if !started {
		t.Fatal("endpoint starter should run before persist")
	}
	snap, _ := s.Snapshot()
	if snap.Status == "prepared" {
		t.Fatal("persist failure published a prepared binding")
	}
}
