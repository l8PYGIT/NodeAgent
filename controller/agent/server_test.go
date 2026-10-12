package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/shared"
	"github.com/pasarguard/node/tenant"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	reg, err := tenant.NewRegistry(tenant.NewMemStore())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	mgr := shared.NewManager(&config.Config{}, reg)
	authn := tenant.NewAuthenticator("master-key", reg)
	return NewServer(reg, mgr, authn)
}

func do(t *testing.T, h http.Handler, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestHealthIsPublic(t *testing.T) {
	h := newTestServer(t).Router()
	if rr := do(t, h, http.MethodGet, "/health", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("health: got %d", rr.Code)
	}
}

func TestAuthScopes(t *testing.T) {
	h := newTestServer(t).Router()

	// Missing key.
	if rr := do(t, h, http.MethodGet, "/admin/tenants", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("no key on admin: got %d, want 401", rr.Code)
	}
	// Tenant key on admin route -> forbidden scope (unknown key resolves to tenant scope, denied at auth).
	if rr := do(t, h, http.MethodGet, "/admin/tenants", "bogus", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("bogus key on admin: got %d, want 401", rr.Code)
	}
	// Master key on admin route -> ok.
	if rr := do(t, h, http.MethodGet, "/admin/tenants", "master-key", nil); rr.Code != http.StatusOK {
		t.Errorf("master key on admin: got %d, want 200", rr.Code)
	}
	// Master key on tenant route -> forbidden scope.
	if rr := do(t, h, http.MethodGet, "/tenant/me", "master-key", nil); rr.Code != http.StatusForbidden {
		t.Errorf("master key on tenant route: got %d, want 403", rr.Code)
	}
}

// Users are stored as "t<tenantID>.<email>", so a dot in a tenant id makes that
// mapping ambiguous: tenant "a" with user "b.x" and tenant "a.b" with user "x"
// both map to "ta.b.x". They would overwrite each other's user records and
// traffic attribution, and the prefix-based stats filter would leak one
// tenant's usage to the other. Such ids are refused at creation.
func TestCreateTenantRejectsAmbiguousID(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	for _, id := range []string{"a.b", "has space", "has/slash", "", strings.Repeat("x", 65)} {
		rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
			"id": id, "api_key": "k-" + id,
		})
		if rr.Code != http.StatusBadRequest {
			t.Errorf("create tenant id=%q: got %d, want 400", id, rr.Code)
		}
	}

	// Ordinary ids still work.
	for _, id := range []string{"t1", "cust-42", "a_b", strings.Repeat("x", 64)} {
		rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
			"id": id, "api_key": "key-" + id,
		})
		if rr.Code != http.StatusCreated {
			t.Errorf("create tenant id=%q: got %d body=%s, want 201", id, rr.Code, rr.Body.String())
		}
	}
}

func TestValidTenantID(t *testing.T) {
	valid := []string{"t1", "A", "cust-42", "a_b", "0", strings.Repeat("x", 64)}
	for _, id := range valid {
		if !validTenantID(id) {
			t.Errorf("validTenantID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "a.b", "a b", "a/b", "a:b", strings.Repeat("x", 65)}
	for _, id := range invalid {
		if validTenantID(id) {
			t.Errorf("validTenantID(%q) = true, want false", id)
		}
	}
}

func TestAdminTenantLifecycle(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	// Create a tenant.
	rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
		"id":          "t1",
		"api_key":     "cust-key",
		"quota_bytes": 1000,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create tenant: got %d body=%s", rr.Code, rr.Body.String())
	}

	// Duplicate -> conflict.
	if rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
		"id": "t1", "api_key": "x",
	}); rr.Code != http.StatusConflict {
		t.Errorf("duplicate tenant: got %d, want 409", rr.Code)
	}

	// Tenant can read its own usage with its key.
	rr = do(t, h, http.MethodGet, "/tenant/me", "cust-key", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("tenant me: got %d body=%s", rr.Code, rr.Body.String())
	}
	var usage usageViewModel
	if err := json.Unmarshal(rr.Body.Bytes(), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.ID != "t1" || usage.QuotaBytes != 1000 || usage.RemainBytes != 1000 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	// Suspend the tenant; its key is then denied.
	if rr := do(t, h, http.MethodPost, "/admin/tenants/t1/suspend", "master-key", nil); rr.Code != http.StatusOK {
		t.Fatalf("suspend: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/tenant/me", "cust-key", nil); rr.Code != http.StatusForbidden {
		t.Errorf("suspended tenant access: got %d, want 403", rr.Code)
	}

	// Resume restores access.
	if rr := do(t, h, http.MethodPost, "/admin/tenants/t1/resume", "master-key", nil); rr.Code != http.StatusOK {
		t.Fatalf("resume: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/tenant/me", "cust-key", nil); rr.Code != http.StatusOK {
		t.Errorf("resumed tenant access: got %d, want 200", rr.Code)
	}

	// Set quota below usage and exhaust -> 402 on tenant access.
	if rr := do(t, h, http.MethodPatch, "/admin/tenants/t1/quota", "master-key", map[string]any{
		"quota_bytes": 10,
	}); rr.Code != http.StatusOK {
		t.Fatalf("set quota: got %d", rr.Code)
	}
	// Drive usage over the new quota via a fresh sync is core-dependent; instead
	// simulate by setting quota to 0 which makes used(0) >= limit(0).
	if rr := do(t, h, http.MethodPatch, "/admin/tenants/t1/quota", "master-key", map[string]any{
		"quota_bytes": 0,
	}); rr.Code != http.StatusOK {
		t.Fatalf("set quota 0: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/tenant/me", "cust-key", nil); rr.Code != http.StatusPaymentRequired {
		t.Errorf("exhausted tenant access: got %d, want 402", rr.Code)
	}

	// Delete the tenant.
	if rr := do(t, h, http.MethodDelete, "/admin/tenants/t1", "master-key", nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d", rr.Code)
	}
	if rr := do(t, h, http.MethodGet, "/tenant/me", "cust-key", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("deleted tenant access: got %d, want 401", rr.Code)
	}
}

func TestAdminCreateTenantExtendedFields(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
		"id":               "t-ext",
		"api_key":          "key-ext",
		"quota_bytes":      5000,
		"allowed_inbounds": []string{"vless-in"},
		"max_users":        5,
		"core_type":        "shared",
		"max_ips":          2,
		"speed_limit_mbps": 100,
		"domains":          []string{"sub.domain.com"},
		"internal_port":    10080,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create tenant: got %d body=%s", rr.Code, rr.Body.String())
	}
	var vm tenantViewModel
	if err := json.Unmarshal(rr.Body.Bytes(), &vm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if vm.MaxUsers != 5 || len(vm.AllowedInbounds) != 1 || vm.AllowedInbounds[0] != "vless-in" ||
		vm.CoreType != "shared" || vm.MaxIPs != 2 || vm.SpeedLimitMbps != 100 ||
		len(vm.Domains) != 1 || vm.Domains[0] != "sub.domain.com" || vm.InternalPort != 10080 {
		t.Fatalf("unexpected tenantViewModel: %+v", vm)
	}
}

func TestCustomerSyncUsersMaxUsersExceeded(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Router()

	// Create tenant with max_users = 1
	rr := do(t, h, http.MethodPost, "/admin/tenants", "master-key", map[string]any{
		"id":          "t-limit",
		"api_key":     "key-limit",
		"quota_bytes": 10000,
		"max_users":   1,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create tenant: got %d body=%s", rr.Code, rr.Body.String())
	}

	// Sync 2 users -> must fail with 400 and "user limit exceeded"
	rr = do(t, h, http.MethodPut, "/tenant/users", "key-limit", map[string]any{
		"users": []map[string]any{
			{"email": "user1", "proxy": map[string]any{"vless_id": "id-1"}},
			{"email": "user2", "proxy": map[string]any{"vless_id": "id-2"}},
		},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] != "user limit exceeded" {
		t.Fatalf("expected error 'user limit exceeded', got %q", resp["error"])
	}
}
