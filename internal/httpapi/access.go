package httpapi

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/riba2534/herdrx/internal/store"
)

var (
	errLoginEnded        = errors.New("Web login ended")
	errAccessUnavailable = errors.New("Web authorization unavailable")
	errWorkbenchStopped  = errors.New("Web workbench stopped")
)

// These leases own Web access only: never a remote Herdr session or process.
type accessLease struct {
	ctx               context.Context
	cancel            context.CancelCauseFunc
	deadlineCancel    context.CancelFunc
	userID, sessionID string
	manager           *accessManager
	once              sync.Once
	hostID            atomic.Pointer[string]
}

type accessManager struct {
	store  *store.Store
	gates  [64]sync.Mutex
	mu     sync.Mutex
	leases map[*accessLease]struct{}
	closed bool
}

func newAccessManager(s *store.Store) *accessManager {
	return &accessManager{store: s, leases: make(map[*accessLease]struct{})}
}
func (m *accessManager) gate(user string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(user))
	return &m.gates[h.Sum32()%uint32(len(m.gates))]
}

func accessCause(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errLoginEnded
	}
	return errAccessUnavailable
}

func (m *accessManager) attach(parent context.Context, userID, sessionID string) (*accessLease, error) {
	g := m.gate(userID)
	g.Lock()
	defer g.Unlock()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	var deadline time.Time
	var err error
	if sessionID == "" {
		err = m.store.UserEnabled(parent, userID)
	} else {
		deadline, err = m.store.SessionAccess(parent, sessionID, userID)
	}
	if err != nil {
		return nil, err
	}
	base := parent
	deadlineCancel := func() {}
	if !deadline.IsZero() {
		base, deadlineCancel = context.WithDeadlineCause(parent, deadline, errLoginEnded)
	}
	ctx, cancel := context.WithCancelCause(base)
	l := &accessLease{ctx: ctx, cancel: cancel, deadlineCancel: deadlineCancel, userID: userID, sessionID: sessionID, manager: m}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(errWorkbenchStopped)
		deadlineCancel()
		return nil, errWorkbenchStopped
	}
	m.leases[l] = struct{}{}
	m.mu.Unlock()
	go l.watch()
	return l, nil
}

func (l *accessLease) watch() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			if l.check() != nil {
				return
			}
		}
	}
}

// Admission and revocation share a short per-user gate. No network I/O runs
// under it. Previously admitted remote actions cannot be recalled or replayed.
func (l *accessLease) check() error {
	g := l.manager.gate(l.userID)
	g.Lock()
	defer g.Unlock()
	if err := l.ctx.Err(); err != nil {
		return context.Cause(l.ctx)
	}
	var err error
	if l.sessionID == "" {
		err = l.manager.store.UserEnabled(l.ctx, l.userID)
	} else {
		_, err = l.manager.store.SessionAccess(l.ctx, l.sessionID, l.userID)
	}
	if err != nil {
		cause := accessCause(err)
		l.cancel(cause)
		return cause
	}
	if hostID := l.hostID.Load(); hostID != nil {
		if _, err := l.manager.store.HostByID(l.ctx, l.userID, *hostID); err != nil {
			cause := accessCause(err)
			l.cancel(cause)
			return cause
		}
	}
	return nil
}

func (l *accessLease) release() {
	l.once.Do(func() {
		l.manager.mu.Lock()
		delete(l.manager.leases, l)
		l.manager.mu.Unlock()
		l.cancel(context.Canceled)
		l.deadlineCancel()
	})
}

// A successful transaction is followed by cancellation before returning to the
// caller. Late connection attachment rechecks SQLite under the same gate.
func (m *accessManager) change(userID, sessionID string, includeBackground bool, mutate func() error) error {
	g := m.gate(userID)
	g.Lock()
	defer g.Unlock()
	if err := mutate(); err != nil {
		return err
	}
	m.mu.Lock()
	for lease := range m.leases {
		if lease.userID != userID {
			continue
		}
		if sessionID != "" && lease.sessionID != sessionID {
			continue
		}
		if lease.sessionID == "" && !includeBackground {
			continue
		}
		lease.cancel(errLoginEnded)
	}
	m.mu.Unlock()
	return nil
}

func (m *accessManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for lease := range m.leases {
		lease.cancel(errWorkbenchStopped)
	}
}
