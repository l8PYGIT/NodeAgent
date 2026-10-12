package tenant

import (
	"path/filepath"
	"testing"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(NewMemStore())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func mustCreate(t *testing.T, r *Registry, p CreateParams) *Tenant {
	t.Helper()
	tn, err := r.CreateTenant(p)
	if err != nil {
		t.Fatalf("CreateTenant(%s): %v", p.ID, err)
	}
	return tn
}

func TestCreateAndAuthorizeActive(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "key-1", QuotaBytes: 1000})

	d := r.Authorize("key-1", 100)
	if !d.Allowed || d.Code != CodeOK {
		t.Fatalf("expected allowed/ok, got %+v", d)
	}
	if d.Tenant == nil || d.Tenant.ID != "t1" {
		t.Fatalf("expected tenant t1, got %+v", d.Tenant)
	}
}

func TestAuthorizeUnknownKey(t *testing.T) {
	r := newTestRegistry(t)
	if d := r.Authorize("nope", 0); d.Allowed || d.Code != CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %+v", d)
	}
}

func TestQuotaExhausted(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 1000})

	if _, err := r.AddUsage("t1", 999); err != nil {
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); !d.Allowed {
		t.Fatalf("999/1000 should be allowed, got %+v", d)
	}
	if _, err := r.AddUsage("t1", 1); err != nil {
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); d.Allowed || d.Code != CodeResourceExhausted || d.Reason != ReasonQuota {
		t.Fatalf("1000/1000 should be exhausted, got %+v", d)
	}
}

func TestCreditLimitAllowsOverage(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 1000, CreditLimitBytes: 500})

	if _, err := r.AddUsage("t1", 1200); err != nil { // over quota, within credit
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); !d.Allowed {
		t.Fatalf("1200 with limit 1500 should be allowed, got %+v", d)
	}
	if _, err := r.AddUsage("t1", 300); err != nil { // reaches 1500
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); d.Allowed || d.Code != CodeResourceExhausted {
		t.Fatalf("1500/1500 should be exhausted, got %+v", d)
	}
}

func TestExpiry(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 1000, ExpireAt: 100})

	if d := r.Authorize("k", 99); !d.Allowed {
		t.Fatalf("before expiry should be allowed, got %+v", d)
	}
	if d := r.Authorize("k", 101); d.Allowed || d.Reason != ReasonExpired {
		t.Fatalf("after expiry should be denied/expired, got %+v", d)
	}
}

func TestSuspendResume(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 1000})

	if _, err := r.Suspend("t1", ReasonManual); err != nil {
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); d.Allowed || d.Code != CodePermissionDenied || d.Reason != ReasonManual {
		t.Fatalf("suspended should be permission_denied/manual, got %+v", d)
	}
	if _, err := r.Resume("t1"); err != nil {
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); !d.Allowed {
		t.Fatalf("resumed should be allowed, got %+v", d)
	}
}

func TestEnforceSuspendsOverQuotaAndExpired(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "quota", APIKey: "kq", QuotaBytes: 100})
	mustCreate(t, r, CreateParams{ID: "exp", APIKey: "ke", QuotaBytes: 100, ExpireAt: 50})
	mustCreate(t, r, CreateParams{ID: "ok", APIKey: "ko", QuotaBytes: 100})

	if _, err := r.AddUsage("quota", 100); err != nil {
		t.Fatal(err)
	}

	changes := r.Enforce(100) // now=100 -> exp is expired
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %+v", len(changes), changes)
	}

	reasons := map[string]Reason{}
	for _, c := range changes {
		reasons[c.Tenant.ID] = c.Reason
	}
	if reasons["quota"] != ReasonQuota {
		t.Errorf("quota tenant reason = %q", reasons["quota"])
	}
	if reasons["exp"] != ReasonExpired {
		t.Errorf("exp tenant reason = %q", reasons["exp"])
	}

	if tn, _ := r.Get("ok"); tn.Status != StatusActive {
		t.Errorf("ok tenant should stay active, got %s", tn.Status)
	}

	// Enforce is idempotent: already-suspended tenants are not re-reported.
	if again := r.Enforce(100); len(again) != 0 {
		t.Errorf("second Enforce should report 0, got %d", len(again))
	}
}

func TestResetPeriod(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 100, PeriodID: 1})
	if _, err := r.AddUsage("t1", 100); err != nil {
		t.Fatal(err)
	}
	r.Enforce(0)
	if d := r.Authorize("k", 0); d.Allowed {
		t.Fatalf("should be suspended before reset, got %+v", d)
	}

	tn, err := r.ResetPeriod("t1")
	if err != nil {
		t.Fatal(err)
	}
	if tn.UsedBytes != 0 || tn.PeriodID != 2 || tn.Status != StatusActive {
		t.Fatalf("after reset: used=%d period=%d status=%s", tn.UsedBytes, tn.PeriodID, tn.Status)
	}
	if d := r.Authorize("k", 0); !d.Allowed {
		t.Fatalf("should be allowed after reset, got %+v", d)
	}
}

func TestConflicts(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k1"})

	if _, err := r.CreateTenant(CreateParams{ID: "t1", APIKey: "k2"}); err != ErrAlreadyExists {
		t.Errorf("duplicate id: expected ErrAlreadyExists, got %v", err)
	}
	if _, err := r.CreateTenant(CreateParams{ID: "t2", APIKey: "k1"}); err != ErrKeyConflict {
		t.Errorf("duplicate key: expected ErrKeyConflict, got %v", err)
	}
}

func TestDeleteRevokesAccess(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k"})
	if err := r.Delete("t1"); err != nil {
		t.Fatal(err)
	}
	if d := r.Authorize("k", 0); d.Allowed || d.Code != CodeUnauthenticated {
		t.Fatalf("deleted tenant key should be unauthenticated, got %+v", d)
	}
	if err := r.Delete("t1"); err != ErrNotFound {
		t.Errorf("re-delete: expected ErrNotFound, got %v", err)
	}
}

func TestAuthenticatorMasterVsTenant(t *testing.T) {
	r := newTestRegistry(t)
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "tenant-key", QuotaBytes: 100})
	a := NewAuthenticator("master-key", r)

	if scope, d := a.Authenticate("master-key", 0); scope != ScopeMaster || !d.Allowed {
		t.Fatalf("master key: scope=%v decision=%+v", scope, d)
	}
	if scope, d := a.Authenticate("tenant-key", 0); scope != ScopeTenant || !d.Allowed {
		t.Fatalf("tenant key: scope=%v decision=%+v", scope, d)
	}
	if scope, d := a.Authenticate("bogus", 0); scope != ScopeTenant || d.Allowed {
		t.Fatalf("bogus key: scope=%v decision=%+v", scope, d)
	}
}

func TestKeysAreHashedNotStored(t *testing.T) {
	r := newTestRegistry(t)
	tn := mustCreate(t, r, CreateParams{ID: "t1", APIKey: "secret-key"})
	if tn.APIKeyHash == "secret-key" || tn.APIKeyHash != HashKey("secret-key") {
		t.Fatalf("api key should be stored as hash, got %q", tn.APIKeyHash)
	}
}

func TestBoltPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenants.db")

	store, err := OpenBoltStore(path)
	if err != nil {
		t.Fatalf("OpenBoltStore: %v", err)
	}
	r, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	mustCreate(t, r, CreateParams{ID: "t1", APIKey: "k", QuotaBytes: 500})
	if _, err := r.AddUsage("t1", 123); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and ensure state survived.
	store2, err := OpenBoltStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	r2, err := NewRegistry(store2)
	if err != nil {
		t.Fatalf("NewRegistry reopen: %v", err)
	}
	tn, ok := r2.Get("t1")
	if !ok {
		t.Fatal("tenant t1 not loaded after reopen")
	}
	if tn.UsedBytes != 123 || tn.QuotaBytes != 500 {
		t.Fatalf("state not persisted: %+v", tn)
	}
	if d := r2.Authorize("k", 0); !d.Allowed {
		t.Fatalf("authorize after reopen should be allowed, got %+v", d)
	}
}

func TestTenantWithExtendedLimits(t *testing.T) {
	r := newTestRegistry(t)
	tn := mustCreate(t, r, CreateParams{
		ID:              "t-ext",
		APIKey:          "key-ext",
		QuotaBytes:      1000,
		AllowedInbounds: []string{"vless-reality", "trojan-ws"},
		MaxUsers:        25,
		CoreType:        "dedicated",
		MaxIPs:          2,
		SpeedLimitMbps:  15,
		Domains:         []string{"tenant.example.com"},
		InternalPort:    10443,
	})

	if tn.MaxUsers != 25 {
		t.Errorf("MaxUsers = %d, want 25", tn.MaxUsers)
	}
	if tn.CoreType != "dedicated" {
		t.Errorf("CoreType = %q, want %q", tn.CoreType, "dedicated")
	}
	if tn.MaxIPs != 2 {
		t.Errorf("MaxIPs = %d, want 2", tn.MaxIPs)
	}
	if tn.SpeedLimitMbps != 15 {
		t.Errorf("SpeedLimitMbps = %d, want 15", tn.SpeedLimitMbps)
	}
	if tn.InternalPort != 10443 {
		t.Errorf("InternalPort = %d, want 10443", tn.InternalPort)
	}
	if len(tn.AllowedInbounds) != 2 || tn.AllowedInbounds[0] != "vless-reality" || tn.AllowedInbounds[1] != "trojan-ws" {
		t.Errorf("AllowedInbounds = %v, want [vless-reality trojan-ws]", tn.AllowedInbounds)
	}
	if len(tn.Domains) != 1 || tn.Domains[0] != "tenant.example.com" {
		t.Errorf("Domains = %v, want [tenant.example.com]", tn.Domains)
	}

	// Verify default CoreType is "shared" when empty
	tnDefault := mustCreate(t, r, CreateParams{
		ID:     "t-default",
		APIKey: "key-default",
	})
	if tnDefault.CoreType != "shared" {
		t.Errorf("default CoreType = %q, want %q", tnDefault.CoreType, "shared")
	}

	// Verify clone deep copy: mutating returned slice does not mutate registry state
	tn.AllowedInbounds[0] = "mutated-inbound"
	tn.Domains[0] = "mutated.example.com"
	fromReg, ok := r.Get("t-ext")
	if !ok {
		t.Fatal("t-ext not found in registry")
	}
	if fromReg.AllowedInbounds[0] != "vless-reality" {
		t.Errorf("AllowedInbounds was mutated: %v", fromReg.AllowedInbounds)
	}
	if fromReg.Domains[0] != "tenant.example.com" {
		t.Errorf("Domains was mutated: %v", fromReg.Domains)
	}

	// Verify SetLimits dynamic update
	updated, err := r.SetLimits("t-ext", []string{"hysteria2"}, 50, "shared", 5, 100, []string{"new.example.com"})
	if err != nil {
		t.Fatalf("SetLimits failed: %v", err)
	}
	if updated.MaxUsers != 50 || updated.CoreType != "shared" || updated.MaxIPs != 5 || updated.SpeedLimitMbps != 100 {
		t.Errorf("SetLimits returned unexpected values: %+v", updated)
	}
	if len(updated.AllowedInbounds) != 1 || updated.AllowedInbounds[0] != "hysteria2" {
		t.Errorf("SetLimits AllowedInbounds = %v", updated.AllowedInbounds)
	}
	if len(updated.Domains) != 1 || updated.Domains[0] != "new.example.com" {
		t.Errorf("SetLimits Domains = %v", updated.Domains)
	}

	// Verify get from registry reflects SetLimits
	fromReg2, _ := r.Get("t-ext")
	if fromReg2.MaxUsers != 50 || fromReg2.CoreType != "shared" || fromReg2.MaxIPs != 5 || fromReg2.SpeedLimitMbps != 100 {
		t.Errorf("Registry state after SetLimits unexpected: %+v", fromReg2)
	}
}

func TestTenantExtendedLimitsBoltPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extended_tenants.db")

	store, err := OpenBoltStore(path)
	if err != nil {
		t.Fatalf("OpenBoltStore: %v", err)
	}
	r, err := NewRegistry(store)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	mustCreate(t, r, CreateParams{
		ID:              "t-ext-bolt",
		APIKey:          "key-bolt",
		QuotaBytes:      2000,
		AllowedInbounds: []string{"vless-reality", "trojan-ws"},
		MaxUsers:        30,
		CoreType:        "dedicated",
		MaxIPs:          4,
		SpeedLimitMbps:  50,
		Domains:         []string{"bolt.example.com"},
		InternalPort:    10444,
	})

	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	// Reopen store from bbolt file and verify all fields survived verbatim
	store2, err := OpenBoltStore(path)
	if err != nil {
		t.Fatalf("reopen bolt store: %v", err)
	}
	defer store2.Close()

	r2, err := NewRegistry(store2)
	if err != nil {
		t.Fatalf("NewRegistry reopen: %v", err)
	}

	tn, ok := r2.Get("t-ext-bolt")
	if !ok {
		t.Fatal("tenant t-ext-bolt not found after reopen")
	}

	if tn.MaxUsers != 30 {
		t.Errorf("persisted MaxUsers = %d, want 30", tn.MaxUsers)
	}
	if tn.CoreType != "dedicated" {
		t.Errorf("persisted CoreType = %q, want %q", tn.CoreType, "dedicated")
	}
	if tn.MaxIPs != 4 {
		t.Errorf("persisted MaxIPs = %d, want 4", tn.MaxIPs)
	}
	if tn.SpeedLimitMbps != 50 {
		t.Errorf("persisted SpeedLimitMbps = %d, want 50", tn.SpeedLimitMbps)
	}
	if tn.InternalPort != 10444 {
		t.Errorf("persisted InternalPort = %d, want 10444", tn.InternalPort)
	}
	if len(tn.AllowedInbounds) != 2 || tn.AllowedInbounds[0] != "vless-reality" || tn.AllowedInbounds[1] != "trojan-ws" {
		t.Errorf("persisted AllowedInbounds = %v, want [vless-reality trojan-ws]", tn.AllowedInbounds)
	}
	if len(tn.Domains) != 1 || tn.Domains[0] != "bolt.example.com" {
		t.Errorf("persisted Domains = %v, want [bolt.example.com]", tn.Domains)
	}
}
