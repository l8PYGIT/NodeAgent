// Package tenant implements the multi-tenant layer for the shared-core node.
//
// A single Xray process is shared by all customers. Tenants are separated by
// the users (emails) they own. The Registry tracks each tenant's quota, usage,
// expiry and status, and decides whether a request is authorized. Enforcement
// is "suspend" (remove the tenant's users from the live core), never destructive
// delete of the tenant's records — so renewals restore users with the same
// credentials. See PRODUCTION_PLAN.md sections 3, 4 and 5.
package tenant

import (
	"errors"
	"sync"
)

// Status is the lifecycle state of a tenant.
type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

// Reason explains why a tenant is suspended.
type Reason string

const (
	ReasonNone    Reason = ""
	ReasonQuota   Reason = "quota"
	ReasonExpired Reason = "expired"
	ReasonManual  Reason = "manual"
)

// Tenant is a single customer's presence on this node.
type Tenant struct {
	ID               string   `json:"id"`
	APIKeyHash       string   `json:"api_key_hash"`
	Status           Status   `json:"status"`
	Reason           Reason   `json:"reason"`
	PeriodID         uint64   `json:"period_id"`
	QuotaBytes       int64    `json:"quota_bytes"`
	UsedBytes        int64    `json:"used_bytes"`
	CreditLimitBytes int64    `json:"credit_limit_bytes"` // extra bytes allowed past quota before suspend
	ExpireAt         int64    `json:"expire_at"`          // unix seconds; 0 = never expires
	AllowedInbounds  []string `json:"allowed_inbounds,omitempty"`
	MaxUsers         int      `json:"max_users,omitempty"`
	CoreType         string   `json:"core_type,omitempty"` // "shared" | "dedicated"
	MaxIPs           int      `json:"max_ips,omitempty"`
	SpeedLimitMbps   int      `json:"speed_limit_mbps,omitempty"`
	Domains          []string `json:"domains,omitempty"`
	InternalPort     int      `json:"internal_port,omitempty"`
}

func (t *Tenant) clone() *Tenant {
	c := *t
	if t.AllowedInbounds != nil {
		c.AllowedInbounds = append([]string(nil), t.AllowedInbounds...)
	}
	if t.Domains != nil {
		c.Domains = append([]string(nil), t.Domains...)
	}
	return &c
}

// effectiveLimit is the quota plus the allowed overage credit. A tenant is
// over-limit only once usage reaches quota + credit.
func (t *Tenant) effectiveLimit() int64 {
	return t.QuotaBytes + t.CreditLimitBytes
}

var (
	ErrNotFound      = errors.New("tenant not found")
	ErrAlreadyExists = errors.New("tenant already exists")
	ErrKeyConflict   = errors.New("api key already in use")
)

// Registry is the thread-safe in-memory index of tenants, backed by a Store
// for persistence across restarts.
type Registry struct {
	mu    sync.RWMutex
	byID  map[string]*Tenant
	byKey map[string]string // apiKeyHash -> tenantID
	store Store
}

// NewRegistry loads persisted tenants from the store and builds the indexes.
func NewRegistry(store Store) (*Registry, error) {
	r := &Registry{
		byID:  make(map[string]*Tenant),
		byKey: make(map[string]string),
		store: store,
	}
	tenants, err := store.LoadTenants()
	if err != nil {
		return nil, err
	}
	for _, t := range tenants {
		r.byID[t.ID] = t
		if t.Status != StatusDeleted {
			r.byKey[t.APIKeyHash] = t.ID
		}
	}
	return r, nil
}

// CreateParams describes a new tenant. APIKey is the raw customer key; it is
// hashed before storage and never persisted in plaintext.
type CreateParams struct {
	ID               string
	APIKey           string
	QuotaBytes       int64
	CreditLimitBytes int64
	ExpireAt         int64
	PeriodID         uint64
	AllowedInbounds  []string
	MaxUsers         int
	CoreType         string
	MaxIPs           int
	SpeedLimitMbps   int
	Domains          []string
	InternalPort     int
}

// CreateTenant registers a new tenant.
func (r *Registry) CreateTenant(p CreateParams) (*Tenant, error) {
	hash := HashKey(p.APIKey)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.byID[p.ID]; ok {
		return nil, ErrAlreadyExists
	}
	if _, ok := r.byKey[hash]; ok {
		return nil, ErrKeyConflict
	}

	period := p.PeriodID
	if period == 0 {
		period = 1
	}

	coreType := p.CoreType
	if coreType == "" {
		coreType = "shared"
	}

	var allowedInbounds []string
	if p.AllowedInbounds != nil {
		allowedInbounds = append([]string(nil), p.AllowedInbounds...)
	}

	var domains []string
	if p.Domains != nil {
		domains = append([]string(nil), p.Domains...)
	}

	t := &Tenant{
		ID:               p.ID,
		APIKeyHash:       hash,
		Status:           StatusActive,
		Reason:           ReasonNone,
		PeriodID:         period,
		QuotaBytes:       p.QuotaBytes,
		CreditLimitBytes: p.CreditLimitBytes,
		ExpireAt:         p.ExpireAt,
		AllowedInbounds:  allowedInbounds,
		MaxUsers:        p.MaxUsers,
		CoreType:        coreType,
		MaxIPs:          p.MaxIPs,
		SpeedLimitMbps:  p.SpeedLimitMbps,
		Domains:         domains,
		InternalPort:    p.InternalPort,
	}

	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	r.byID[t.ID] = t
	r.byKey[hash] = t.ID
	return t.clone(), nil
}

// Get returns a copy of the tenant by id.
func (r *Registry) Get(id string) (*Tenant, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, false
	}
	return t.clone(), true
}

// List returns copies of all tenants.
func (r *Registry) List() []*Tenant {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tenant, 0, len(r.byID))
	for _, t := range r.byID {
		out = append(out, t.clone())
	}
	return out
}

// AddUsage adds a non-negative byte delta to the tenant's cumulative usage.
func (r *Registry) AddUsage(id string, delta int64) (*Tenant, error) {
	if delta < 0 {
		delta = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.UsedBytes += delta
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// SetQuota updates quota, credit limit and expiry for the current period.
func (r *Registry) SetQuota(id string, quotaBytes, creditLimitBytes, expireAt int64) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.QuotaBytes = quotaBytes
	t.CreditLimitBytes = creditLimitBytes
	t.ExpireAt = expireAt
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// SetLimits updates allowed inbounds, max users, core type, max IPs, speed limit and domains.
func (r *Registry) SetLimits(id string, allowedInbounds []string, maxUsers int, coreType string, maxIPs, speedLimitMbps int, domains []string) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	if coreType == "" {
		coreType = "shared"
	}
	var inboundsCopy []string
	if allowedInbounds != nil {
		inboundsCopy = append([]string(nil), allowedInbounds...)
	}
	var domainsCopy []string
	if domains != nil {
		domainsCopy = append([]string(nil), domains...)
	}
	t.AllowedInbounds = inboundsCopy
	t.MaxUsers = maxUsers
	t.CoreType = coreType
	t.MaxIPs = maxIPs
	t.SpeedLimitMbps = speedLimitMbps
	t.Domains = domainsCopy
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// Suspend marks a tenant suspended with a reason (idempotent).
func (r *Registry) Suspend(id string, reason Reason) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.Status = StatusSuspended
	t.Reason = reason
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// Resume reactivates a suspended tenant.
func (r *Registry) Resume(id string) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.Status = StatusActive
	t.Reason = ReasonNone
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// ResetPeriod starts a new billing period: usage is zeroed, the tenant is
// reactivated, and the period id is incremented.
func (r *Registry) ResetPeriod(id string) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	t.UsedBytes = 0
	t.PeriodID++
	t.Status = StatusActive
	t.Reason = ReasonNone
	if err := r.store.SaveTenant(t); err != nil {
		return nil, err
	}
	return t.clone(), nil
}

// Delete permanently removes a tenant and its key mapping.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byID[id]
	if !ok {
		return ErrNotFound
	}
	if err := r.store.DeleteTenant(id); err != nil {
		return err
	}
	delete(r.byKey, t.APIKeyHash)
	delete(r.byID, id)
	return nil
}

// Change records a tenant whose status changed during enforcement.
type Change struct {
	Tenant *Tenant
	Reason Reason
}

// Enforce scans active tenants and suspends any that are expired or over their
// effective limit. It returns the tenants that changed so the caller can remove
// their users from the live core and report the suspension.
func (r *Registry) Enforce(now int64) []Change {
	r.mu.Lock()
	defer r.mu.Unlock()

	var changes []Change
	for _, t := range r.byID {
		if t.Status != StatusActive {
			continue
		}
		var reason Reason
		switch {
		case t.ExpireAt > 0 && now > t.ExpireAt:
			reason = ReasonExpired
		case t.UsedBytes >= t.effectiveLimit():
			reason = ReasonQuota
		}
		if reason == ReasonNone {
			continue
		}
		t.Status = StatusSuspended
		t.Reason = reason
		_ = r.store.SaveTenant(t)
		changes = append(changes, Change{Tenant: t.clone(), Reason: reason})
	}
	return changes
}
