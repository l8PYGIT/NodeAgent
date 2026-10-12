package tenant

import "sync"

// Store persists tenants so enforcement survives node restarts. The node keeps
// its own copy of tenant quota/usage/status/limits (the local source of truth for
// enforcement), independent of the main panel.
type Store interface {
	LoadTenants() ([]*Tenant, error)
	SaveTenant(*Tenant) error
	DeleteTenant(id string) error
	Close() error
}

// memStore is an in-memory Store for tests and ephemeral use. It stores copies
// so callers cannot mutate persisted state by reference.
type memStore struct {
	mu sync.Mutex
	m  map[string]*Tenant
}

// NewMemStore returns an in-memory Store.
func NewMemStore() Store {
	return &memStore{m: make(map[string]*Tenant)}
}

func (s *memStore) LoadTenants() ([]*Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Tenant, 0, len(s.m))
	for _, t := range s.m {
		out = append(out, t.clone())
	}
	return out, nil
}

func (s *memStore) SaveTenant(t *Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[t.ID] = t.clone()
	return nil
}

func (s *memStore) DeleteTenant(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *memStore) Close() error { return nil }
