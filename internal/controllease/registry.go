package controllease

import (
	"sync"
	"time"
)

type Lease struct {
	Owner     string
	ExpiresAt time.Time
}

type Registry struct {
	mu     sync.Mutex
	leases map[string]Lease
}

func New() *Registry { return &Registry{leases: make(map[string]Lease)} }

func (r *Registry) Acquire(key, owner string, ttl time.Duration) (Lease, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	current, exists := r.leases[key]
	if exists && current.ExpiresAt.After(now) && current.Owner != owner {
		return current, false
	}
	lease := Lease{Owner: owner, ExpiresAt: now.Add(ttl)}
	r.leases[key] = lease
	return lease, true
}

func (r *Registry) Renew(key, owner string, ttl time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.leases[key]
	if !exists || current.Owner != owner || current.ExpiresAt.Before(time.Now()) {
		return false
	}
	current.ExpiresAt = time.Now().Add(ttl)
	r.leases[key] = current
	return true
}

func (r *Registry) Valid(key, owner string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.leases[key]
	if exists && current.ExpiresAt.Before(time.Now()) {
		delete(r.leases, key)
		return false
	}
	return exists && current.Owner == owner
}

func (r *Registry) Release(key, owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, exists := r.leases[key]; exists && current.Owner == owner {
		delete(r.leases, key)
	}
}
