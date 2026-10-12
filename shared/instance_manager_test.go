package shared

import (
	"context"
	"fmt"
	"testing"

	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/tenant"
)

func TestInstanceManagerPortAllocationAndRelease(t *testing.T) {
	// 1. Default basePort when <= 0
	imDefault := NewInstanceManager(0)
	if imDefault.basePort != 11000 {
		t.Fatalf("expected default basePort 11000, got %d", imDefault.basePort)
	}

	im := NewInstanceManager(11000)

	// 2. Allocate port for first tenant
	port1, err := im.AllocatePort("tenant-1")
	if err != nil {
		t.Fatalf("AllocatePort failed: %v", err)
	}
	if port1 != 11000 {
		t.Fatalf("expected port 11000, got %d", port1)
	}

	// 3. Re-allocating for the same tenant should reuse the port
	port1Reuse, err := im.AllocatePort("tenant-1")
	if err != nil {
		t.Fatalf("AllocatePort reuse failed: %v", err)
	}
	if port1Reuse != port1 {
		t.Fatalf("expected reused port %d, got %d", port1, port1Reuse)
	}

	// 4. Allocate port for second tenant
	port2, err := im.AllocatePort("tenant-2")
	if err != nil {
		t.Fatalf("AllocatePort for tenant-2 failed: %v", err)
	}
	if port2 != 11001 {
		t.Fatalf("expected port 11001, got %d", port2)
	}

	// 5. Release tenant-1 port and verify it can be re-allocated
	im.ReleasePort("tenant-1")

	port3, err := im.AllocatePort("tenant-3")
	if err != nil {
		t.Fatalf("AllocatePort for tenant-3 failed: %v", err)
	}
	if port3 != 11000 {
		t.Fatalf("expected re-allocated port 11000 for tenant-3, got %d", port3)
	}
}

func TestInstanceManagerPortRangeExhaustion(t *testing.T) {
	im := NewInstanceManager(11000)

	// Allocate all 1000 ports
	for i := 0; i < 1000; i++ {
		tid := fmt.Sprintf("tenant-%d", i)
		p, err := im.AllocatePort(tid)
		if err != nil {
			t.Fatalf("failed to allocate port %d for %s: %v", 11000+i, tid, err)
		}
		if p != 11000+i {
			t.Fatalf("expected port %d, got %d", 11000+i, p)
		}
	}

	// 1001st allocation must fail
	_, err := im.AllocatePort("tenant-overflow")
	if err == nil {
		t.Fatal("expected error when port range [11000, 12000) is exhausted, got nil")
	}
}

func TestDedicatedInstanceLifecycle(t *testing.T) {
	ctx := context.Background()
	im := NewInstanceManager(11000)

	inst, err := im.GetOrCreateInstance("tenant-ded-1")
	if err != nil {
		t.Fatalf("GetOrCreateInstance failed: %v", err)
	}
	if inst.TenantID != "tenant-ded-1" {
		t.Fatalf("expected tenant-ded-1, got %s", inst.TenantID)
	}
	if inst.Port != 11000 {
		t.Fatalf("expected port 11000, got %d", inst.Port)
	}
	if inst.Started {
		t.Fatal("expected instance not to be started initially")
	}

	validCfg := `{"inbounds":[]}`
	if err := inst.Start(ctx, validCfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !inst.Started {
		t.Fatal("expected instance to be started")
	}
	if inst.ConfigJSON != validCfg {
		t.Fatalf("expected config %s, got %s", validCfg, inst.ConfigJSON)
	}

	newCfg := `{"inbounds":[{"tag":"vless-1"}]}`
	if err := inst.ApplyConfig(ctx, newCfg); err != nil {
		t.Fatalf("ApplyConfig failed: %v", err)
	}
	if inst.ConfigJSON != newCfg {
		t.Fatalf("expected config %s, got %s", newCfg, inst.ConfigJSON)
	}
	if !inst.Started {
		t.Fatal("expected instance to remain started after ApplyConfig")
	}

	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if inst.Started {
		t.Fatal("expected instance to be stopped")
	}
}

func TestGenerateSNIRoutingRules(t *testing.T) {
	sharedPort := 10000
	tenantDomains := map[string][]string{
		"tenant-1": {"t1.example.com", "sub.t1.example.com"},
		"tenant-2": {"t2.example.com"},
	}
	tenantPorts := map[string]int{
		"tenant-1": 11001,
		"tenant-2": 11002,
	}

	routingConfig := GenerateSNIRoutingRules(sharedPort, tenantDomains, tenantPorts)
	if routingConfig == nil {
		t.Fatal("expected non-nil routingConfig")
	}

	rulesRaw, ok := routingConfig["rules"]
	if !ok {
		t.Fatal("expected 'rules' field in routingConfig")
	}
	rules, ok := rulesRaw.([]map[string]any)
	if !ok {
		t.Fatalf("expected rules to be []map[string]any, got %T", rulesRaw)
	}

	outboundsRaw, ok := routingConfig["outbounds"]
	if !ok {
		t.Fatal("expected 'outbounds' field in routingConfig")
	}
	outbounds, ok := outboundsRaw.([]map[string]any)
	if !ok {
		t.Fatalf("expected outbounds to be []map[string]any, got %T", outboundsRaw)
	}

	// Must have rules for tenant-1, tenant-2, plus fallback rule for sharedPort
	if len(rules) < 3 {
		t.Fatalf("expected at least 3 rules (2 tenants + 1 fallback), got %d", len(rules))
	}

	// Verify tenant-1 rule and outbound
	var foundT1Rule, foundT2Rule, foundFallbackRule bool
	for _, r := range rules {
		tag, _ := r["outboundTag"].(string)
		if tag == "outbound-tenant-1" {
			foundT1Rule = true
			domains, _ := r["domain"].([]string)
			if len(domains) != 2 || domains[0] != "t1.example.com" {
				t.Fatalf("unexpected domains for tenant-1: %v", domains)
			}
		}
		if tag == "outbound-tenant-2" {
			foundT2Rule = true
			domains, _ := r["domain"].([]string)
			if len(domains) != 1 || domains[0] != "t2.example.com" {
				t.Fatalf("unexpected domains for tenant-2: %v", domains)
			}
		}
		if tag == "outbound-shared" {
			foundFallbackRule = true
		}
	}

	if !foundT1Rule {
		t.Fatal("missing rule for outbound-tenant-1")
	}
	if !foundT2Rule {
		t.Fatal("missing rule for outbound-tenant-2")
	}
	if !foundFallbackRule {
		t.Fatal("missing fallback rule for outbound-shared")
	}

	// Verify outbounds
	var foundT1Out, foundT2Out, foundSharedOut bool
	for _, ob := range outbounds {
		tag, _ := ob["tag"].(string)
		settings, _ := ob["settings"].(map[string]any)
		redirect, _ := settings["redirect"].(string)

		if tag == "outbound-tenant-1" {
			foundT1Out = true
			if redirect != "127.0.0.1:11001" {
				t.Fatalf("expected redirect 127.0.0.1:11001, got %s", redirect)
			}
		}
		if tag == "outbound-tenant-2" {
			foundT2Out = true
			if redirect != "127.0.0.1:11002" {
				t.Fatalf("expected redirect 127.0.0.1:11002, got %s", redirect)
			}
		}
		if tag == "outbound-shared" {
			foundSharedOut = true
			if redirect != "127.0.0.1:10000" {
				t.Fatalf("expected redirect 127.0.0.1:10000, got %s", redirect)
			}
		}
	}

	if !foundT1Out {
		t.Fatal("missing outbound for outbound-tenant-1")
	}
	if !foundT2Out {
		t.Fatal("missing outbound for outbound-tenant-2")
	}
	if !foundSharedOut {
		t.Fatal("missing outbound for outbound-shared")
	}
}

func TestSharedManagerDedicatedInstanceIntegration(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.NewMemStore())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	mgr := NewManager(&config.Config{}, reg)

	if mgr.InstanceManager() == nil {
		t.Fatal("expected non-nil InstanceManager from Manager")
	}

	ctx := context.Background()
	cfg := `{"inbounds":[]}`

	// Apply dedicated config
	if err := mgr.ApplyDedicatedConfig(ctx, "tenant-ded", cfg); err != nil {
		t.Fatalf("ApplyDedicatedConfig failed: %v", err)
	}

	inst := mgr.InstanceManager().GetInstance("tenant-ded")
	if inst == nil {
		t.Fatal("expected dedicated instance to be registered in InstanceManager")
	}
	if !inst.Started {
		t.Fatal("expected dedicated instance to be started")
	}
	if inst.ConfigJSON != cfg {
		t.Fatalf("expected config %s, got %s", cfg, inst.ConfigJSON)
	}

	// Suspend dedicated instance
	if err := mgr.SuspendDedicatedInstance(ctx, "tenant-ded"); err != nil {
		t.Fatalf("SuspendDedicatedInstance failed: %v", err)
	}
	if inst.Started {
		t.Fatal("expected dedicated instance to be stopped after SuspendDedicatedInstance")
	}

	// Resume dedicated instance
	if err := mgr.ResumeDedicatedInstance(ctx, "tenant-ded"); err != nil {
		t.Fatalf("ResumeDedicatedInstance failed: %v", err)
	}
	if !inst.Started {
		t.Fatal("expected dedicated instance to be started after ResumeDedicatedInstance")
	}
}
