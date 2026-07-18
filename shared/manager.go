// Package shared implements the shared-core multi-tenant manager: a single Xray
// process serves all tenants, who are separated by the users (emails) they own.
//
// It ties together the tenant.Registry (quota/status/expiry, the local source of
// truth for enforcement) and the existing backend/xray core (runtime add/remove
// user and per-user stats). Enforcement is non-destructive: suspending a tenant
// removes its users from the live core but keeps their records, so a resume or
// renewal restores them with the same credentials.
//
// Note: time limits and lifecycle of individual end-users are the customer's
// responsibility (managed by their own panel). The node only enforces the
// buyer/tenant-level quota and expiry.
package shared

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"golang.org/x/crypto/curve25519"

	"github.com/pasarguard/node/backend/xray"
	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
	"github.com/pasarguard/node/tenant"
)

// Manager owns the single shared Xray core and routes per-tenant user
// operations to it.
type Manager struct {
	mu            sync.Mutex
	cfg           *config.Config
	reg           *tenant.Registry
	core          *xray.Xray
	users         map[string]map[string]*common.User // tenantID -> namespacedEmail -> user
	emailTo       map[string]string                  // namespacedEmail -> tenantID
	creds         map[string]string                  // credential -> namespacedEmail (owner)
	forceInbounds []string                           // operator override: if set, every user is applied to these inbound tags
	autoInbounds  []string                           // proxy inbound tags derived from the running config (fallback when forceInbounds is empty)
	lastSeen      map[string]int64                   // namespacedEmail -> last absolute traffic bytes (for delta accounting)
	config        string                             // the raw JSON of the running core config
}

func NewManager(cfg *config.Config, reg *tenant.Registry) *Manager {
	return &Manager{
		cfg:      cfg,
		reg:      reg,
		users:    make(map[string]map[string]*common.User),
		emailTo:  make(map[string]string),
		creds:    make(map[string]string),
		lastSeen: make(map[string]int64),
	}
}

// SetForceInbounds sets an operator override: every tenant user is applied to
// the given inbound tags regardless of the tags the customer's panel sends. When
// left empty, the manager instead auto-derives the tags from the running config
// (see effectiveInbounds), which is the normal case. An explicit override only
// makes sense if the operator wants to pin users to a subset of the node's
// proxy inbounds.
func (m *Manager) SetForceInbounds(tags []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forceInbounds = tags
}

// effectiveInbounds returns the inbound tags every tenant user must be applied
// to. Callers hold m.mu. The operator override wins; otherwise the tags derived
// from the running config are used. The customer panel's own tags are never
// trusted because they rarely match the node's real inbound names.
func (m *Manager) effectiveInbounds() []string {
	if len(m.forceInbounds) > 0 {
		return m.forceInbounds
	}
	return m.autoInbounds
}

// buildUser returns the namespaced user to apply to the core, pinning it to the
// node's effective inbound tags so it lands on the real shared inbound(s).
func (m *Manager) buildUser(tenantID string, u *common.User) *common.User {
	inbounds := u.GetInbounds()
	if eff := m.effectiveInbounds(); len(eff) > 0 {
		inbounds = eff
	}
	return &common.User{
		Email:    namespacedEmail(tenantID, u.GetEmail()),
		Proxies:  u.GetProxies(),
		Inbounds: inbounds,
	}
}

// userCredentials returns the protocol credentials a user carries. Credentials
// must be globally unique on a node: in a shared inbound the uuid/password is the
// actual authentication secret, so a collision across tenants would break both
// isolation and traffic accounting.
func userCredentials(u *common.User) []string {
	p := u.GetProxies()
	if p == nil {
		return nil
	}
	var out []string
	if v := p.GetVless(); v != nil && v.GetId() != "" {
		out = append(out, "vless:"+v.GetId())
	}
	if v := p.GetVmess(); v != nil && v.GetId() != "" {
		out = append(out, "vmess:"+v.GetId())
	}
	if t := p.GetTrojan(); t != nil && t.GetPassword() != "" {
		out = append(out, "trojan:"+t.GetPassword())
	}
	if s := p.GetShadowsocks(); s != nil && s.GetPassword() != "" {
		out = append(out, "ss:"+s.GetPassword())
	}
	if h := p.GetHysteria(); h != nil && h.GetAuth() != "" {
		out = append(out, "hy:"+h.GetAuth())
	}
	return out
}

// StartCore brings up the shared Xray with a fixed operator-provided config.
func (m *Manager) StartCore(ctx context.Context, configJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.core != nil {
		return fmt.Errorf("core already started")
	}
	return m.startCoreLocked(ctx, configJSON)
}

// ApplyConfig (re)starts the shared core with a new fixed config and re-applies
// the users of all currently active tenants. Used by the master to push config.
func (m *Manager) ApplyConfig(ctx context.Context, configJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopLocked()
	if err := m.startCoreLocked(ctx, configJSON); err != nil {
		return err
	}

	for tenantID := range m.users {
		tn, ok := m.reg.Get(tenantID)
		if !ok || tn.Status != tenant.StatusActive {
			continue
		}
		if err := m.addTenantToCore(ctx, tenantID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) startCoreLocked(ctx context.Context, configJSON string) error {
	xcfg, err := xray.NewConfig(configJSON, nil)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	apiPort := netutil.FindFreePort()
	metricPort := netutil.FindFreePort()

	core, err := xray.New(ctx, xcfg, nil, apiPort, metricPort, m.cfg)
	if err != nil {
		return fmt.Errorf("start core: %w", err)
	}
	m.core = core
	m.config = configJSON
	m.autoInbounds = proxyInboundTags(configJSON)
	if len(m.forceInbounds) == 0 && len(m.autoInbounds) == 0 {
		log.Printf("shared core: warning: config has no proxy inbounds; tenant users will match no inbound and clients will fail to connect")
	}
	return nil
}

// proxyInboundTags returns the tags of every proxy inbound in a core config.
// Non-proxy inbounds (e.g. dokodemo-door API listeners) are skipped because a
// tenant user carries no account for them. Tenant users are pinned to these tags
// so they land on the node's real inbound(s) no matter what tags the customer's
// panel used. Unparseable input yields nil.
func proxyInboundTags(configJSON string) []string {
	var doc struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &doc); err != nil {
		return nil
	}
	proxy := map[string]bool{
		"vmess": true, "vless": true, "trojan": true,
		"shadowsocks": true, "hysteria": true,
	}
	var tags []string
	for _, ib := range doc.Inbounds {
		if ib.Tag != "" && proxy[strings.ToLower(ib.Protocol)] {
			tags = append(tags, ib.Tag)
		}
	}
	return tags
}

// Started reports whether the shared core process is running.
func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.core != nil && m.core.Started()
}

// TenantUserCount returns how many users a tenant currently has registered.
func (m *Manager) TenantUserCount(tenantID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users[tenantID])
}

// Version returns the running core version.
func (m *Manager) Version() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.core == nil {
		return ""
	}
	return m.core.Version()
}

// Config returns the raw JSON of the currently running core config (empty if the
// core has not been started). For the operator/master only — it may contain
// outbound credentials and routing that must not be shared with customers.
func (m *Manager) Config() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

// RestartCore re-applies the currently running config (stop + start + re-add
// active tenants' users). Returns an error if no config has been set yet.
func (m *Manager) RestartCore(ctx context.Context) error {
	m.mu.Lock()
	cfg := m.config
	m.mu.Unlock()
	if cfg == "" {
		return fmt.Errorf("no config to (re)start; push a config first")
	}
	return m.ApplyConfig(ctx, cfg)
}

// XrayExecutablePath returns the path of the Xray binary the core runs.
func (m *Manager) XrayExecutablePath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.XrayExecutablePath
}

// SharableInbounds returns a {"inbounds":[...]} JSON document containing only the
// inbound definitions a customer needs to replicate the node's connection in
// their own panel. It is narrowed to the effective inbound tags (operator
// override, else the proxy inbounds derived from the config) so the customer
// sees exactly the inbound(s) its users actually land on. Outbounds, routing and
// other operator-only sections are intentionally omitted.
func (m *Manager) SharableInbounds() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config == "" {
		return `{"inbounds":[]}`, nil
	}
	var doc struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(m.config), &doc); err != nil {
		return "", fmt.Errorf("parse config: %w", err)
	}
	keep := doc.Inbounds
	if eff := m.effectiveInbounds(); len(eff) > 0 {
		allowed := make(map[string]bool, len(eff))
		for _, t := range eff {
			allowed[t] = true
		}
		filtered := make([]json.RawMessage, 0, len(doc.Inbounds))
		for _, ib := range doc.Inbounds {
			var meta struct {
				Tag string `json:"tag"`
			}
			if err := json.Unmarshal(ib, &meta); err == nil && allowed[meta.Tag] {
				filtered = append(filtered, ib)
			}
		}
		// Only narrow when at least one effective tag actually matched; otherwise
		// fall back to all inbounds so the customer still gets something usable.
		if len(filtered) > 0 {
			keep = filtered
		}
	}
	// Redact node secrets (e.g. the Reality private key) and surface the public
	// key the customer needs to build client links.
	redacted := make([]json.RawMessage, 0, len(keep))
	for _, ib := range keep {
		redacted = append(redacted, redactInboundSecrets(ib))
	}
	out, err := json.Marshal(struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}{Inbounds: redacted})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// redactInboundSecrets removes server-only secrets from an inbound definition
// before sharing it with a customer. For Reality it strips privateKey and adds
// the derived publicKey (which clients need). Unparseable input is returned
// unchanged.
func redactInboundSecrets(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	if ss, ok := m["streamSettings"].(map[string]any); ok {
		if rs, ok := ss["realitySettings"].(map[string]any); ok {
			if pk, ok := rs["privateKey"].(string); ok && pk != "" {
				if pub, ok := realityPublicKey(pk); ok {
					rs["publicKey"] = pub
				}
				delete(rs, "privateKey")
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// realityPublicKey derives the X25519 public key (base64 raw-url) from a Reality
// private key as produced by `xray x25519`.
func realityPublicKey(privB64 string) (string, bool) {
	priv, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(privB64, "="))
	if err != nil || len(priv) != 32 {
		return "", false
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(pub), true
}

// Stop shuts the shared core down.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *Manager) stopLocked() {
	if m.core != nil {
		m.core.Shutdown()
		m.core = nil
	}
}

// SetTenantUsers replaces the user set owned by a tenant. Emails are namespaced
// per tenant to avoid collisions across customers. Users are applied to the live
// core only if the tenant is currently active.
func (m *Manager) SetTenantUsers(ctx context.Context, tenantID string, users []*common.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tn, ok := m.reg.Get(tenantID)
	if !ok {
		return tenant.ErrNotFound
	}
	active := tn.Status == tenant.StatusActive
	prefix := "t" + tenantID + "."

	newSet := make(map[string]*common.User, len(users))
	for _, u := range users {
		ne := namespacedEmail(tenantID, u.GetEmail())
		newSet[ne] = m.buildUser(tenantID, u)
	}

	// Enforce credential uniqueness: no duplicates within this batch, and no
	// collision with a credential already owned by another tenant.
	batch := make(map[string]string)
	for email, u := range newSet {
		for _, cred := range userCredentials(u) {
			if other, dup := batch[cred]; dup && other != email {
				return fmt.Errorf("duplicate credential shared by users %q and %q", email, other)
			}
			batch[cred] = email
			if owner, exists := m.creds[cred]; exists && !strings.HasPrefix(owner, prefix) {
				return fmt.Errorf("credential already in use by another tenant")
			}
		}
	}

	// Remove users that are no longer present.
	for email, u := range m.users[tenantID] {
		if _, keep := newSet[email]; keep {
			continue
		}
		_ = m.removeFromCore(ctx, u)
		delete(m.emailTo, email)
		delete(m.lastSeen, email)
		for _, cred := range userCredentials(u) {
			delete(m.creds, cred)
		}
	}

	// Add or refresh users.
	for email, u := range newSet {
		m.emailTo[email] = tenantID
		for _, cred := range userCredentials(u) {
			m.creds[cred] = email
		}
		if active && m.core != nil {
			if err := m.core.SyncUser(ctx, u); err != nil {
				return fmt.Errorf("sync user %q: %w", email, err)
			}
		}
	}

	m.users[tenantID] = newSet
	return nil
}

// AddTenantUsers merges users into a tenant's set (add/update) without removing
// others. Used for incremental SyncUser streams from a customer's panel.
func (m *Manager) AddTenantUsers(ctx context.Context, tenantID string, users []*common.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tn, ok := m.reg.Get(tenantID)
	if !ok {
		return tenant.ErrNotFound
	}
	active := tn.Status == tenant.StatusActive
	prefix := "t" + tenantID + "."

	if m.users[tenantID] == nil {
		m.users[tenantID] = make(map[string]*common.User)
	}

	for _, u := range users {
		nu := m.buildUser(tenantID, u)
		ne := nu.GetEmail()
		for _, cred := range userCredentials(nu) {
			if owner, exists := m.creds[cred]; exists && owner != ne && !strings.HasPrefix(owner, prefix) {
				return fmt.Errorf("credential already in use by another tenant")
			}
		}
		m.users[tenantID][ne] = nu
		m.emailTo[ne] = tenantID
		for _, cred := range userCredentials(nu) {
			m.creds[cred] = ne
		}
		if active && m.core != nil {
			if err := m.core.SyncUser(ctx, nu); err != nil {
				return fmt.Errorf("sync user %q: %w", ne, err)
			}
		}
	}
	return nil
}

// SuspendTenant marks a tenant suspended and removes its users from the live
// core (their connections drop). Records are retained for later resume.
func (m *Manager) SuspendTenant(ctx context.Context, tenantID string, reason tenant.Reason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.reg.Suspend(tenantID, reason); err != nil {
		return err
	}
	m.removeTenantFromCore(ctx, tenantID)
	return nil
}

// ResumeTenant reactivates a tenant and re-adds its users to the live core.
func (m *Manager) ResumeTenant(ctx context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.reg.Resume(tenantID); err != nil {
		return err
	}
	return m.addTenantToCore(ctx, tenantID)
}

// DeleteTenant permanently removes a tenant: its users are removed from the live
// core and all of its records are dropped.
func (m *Manager) DeleteTenant(ctx context.Context, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeTenantFromCore(ctx, tenantID)
	if err := m.reg.Delete(tenantID); err != nil {
		return err
	}
	for email, u := range m.users[tenantID] {
		delete(m.emailTo, email)
		delete(m.lastSeen, email)
		for _, cred := range userCredentials(u) {
			delete(m.creds, cred)
		}
	}
	delete(m.users, tenantID)
	return nil
}

// CollectAndEnforce polls per-user traffic, attributes it to tenants, applies
// quota/expiry enforcement, and removes suspended tenants' users from the core.
// It is intended to be called periodically by a background loop.
func (m *Manager) CollectAndEnforce(ctx context.Context, now int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.core == nil {
		return nil
	}

	// Read absolute cumulative per-user counters WITHOUT resetting, so an
	// external panel (PasarGuard) can also read them. We track the last value
	// per user and add only the delta to the tenant's usage.
	resp, err := m.core.GetStats(ctx, &common.StatRequest{
		Type:   common.StatType_UsersStat,
		Reset_: false,
	})
	if err == nil && resp != nil {
		current := make(map[string]int64)
		for _, s := range resp.GetStats() {
			current[s.GetName()] += s.GetValue() // sum uplink+downlink rows
		}
		deltas := make(map[string]int64)
		for email, total := range current {
			last := m.lastSeen[email]
			var delta int64
			if total >= last {
				delta = total - last
			} else {
				delta = total // core restarted; counters reset to 0
			}
			m.lastSeen[email] = total
			if tid, ok := m.emailTo[email]; ok {
				deltas[tid] += delta
			}
		}
		for tid, d := range deltas {
			if _, err := m.reg.AddUsage(tid, d); err != nil {
				return fmt.Errorf("add usage %q: %w", tid, err)
			}
		}
	}

	for _, c := range m.reg.Enforce(now) {
		m.removeTenantFromCore(ctx, c.Tenant.ID)
	}
	return nil
}

// GetTenantUserStats returns the live per-user stats for a tenant with emails
// de-namespaced back to their original form, so a customer's panel sees its own
// user names. Read-only (no reset).
func (m *Manager) GetTenantUserStats(ctx context.Context, tenantID string) (*common.StatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := &common.StatResponse{}
	if m.core == nil {
		return out, nil
	}
	resp, err := m.core.GetStats(ctx, &common.StatRequest{Type: common.StatType_UsersStat, Reset_: false})
	if err != nil || resp == nil {
		return out, nil
	}
	prefix := "t" + tenantID + "."
	for _, s := range resp.GetStats() {
		if !strings.HasPrefix(s.GetName(), prefix) {
			continue
		}
		out.Stats = append(out.Stats, &common.Stat{
			Name:  strings.TrimPrefix(s.GetName(), prefix),
			Type:  s.GetType(),
			Link:  s.GetLink(),
			Value: s.GetValue(),
		})
	}
	return out, nil
}

// --- internal helpers (callers hold m.mu) ---

func (m *Manager) removeTenantFromCore(ctx context.Context, tenantID string) {
	if m.core == nil {
		return
	}
	for _, u := range m.users[tenantID] {
		_ = m.removeFromCore(ctx, u)
	}
}

func (m *Manager) addTenantToCore(ctx context.Context, tenantID string) error {
	if m.core == nil {
		return nil
	}
	for email, u := range m.users[tenantID] {
		if err := m.core.SyncUser(ctx, u); err != nil {
			return fmt.Errorf("sync user %q: %w", email, err)
		}
	}
	return nil
}

// removeFromCore removes a user from every inbound by syncing it with no
// inbounds, which the backend translates into RemoveInboundUser calls.
func (m *Manager) removeFromCore(ctx context.Context, u *common.User) error {
	if m.core == nil {
		return nil
	}
	return m.core.UpdateUsers(ctx, []*common.User{withInbounds(u, nil)})
}

func namespacedEmail(tenantID, email string) string {
	return "t" + tenantID + "." + email
}

func withInbounds(u *common.User, inbounds []string) *common.User {
	return &common.User{
		Email:    u.GetEmail(),
		Proxies:  u.GetProxies(),
		Inbounds: inbounds,
	}
}
