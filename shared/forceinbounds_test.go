package shared

import (
	"slices"
	"testing"

	"github.com/pasarguard/node/common"
)

// TestProxyInboundTags checks that only proxy inbounds are derived from a config
// and non-proxy inbounds (e.g. the api dokodemo-door) are skipped.
func TestProxyInboundTags(t *testing.T) {
	cfg := `{
		"inbounds": [
			{"tag":"vless-reality","port":443,"protocol":"vless"},
			{"tag":"trojan-in","port":8443,"protocol":"trojan"},
			{"tag":"api","port":10085,"protocol":"dokodemo-door"}
		]
	}`
	got := proxyInboundTags(cfg)
	want := []string{"vless-reality", "trojan-in"}
	if !slices.Equal(got, want) {
		t.Fatalf("proxyInboundTags = %v, want %v", got, want)
	}

	if tags := proxyInboundTags("not json"); tags != nil {
		t.Fatalf("invalid config should yield nil, got %v", tags)
	}
}

// TestBuildUserPinsToAutoInbounds reproduces the original EOF bug: after the
// operator pushes a config whose inbound tag is NOT the hardcoded "vless-in",
// tenant users must still be pinned to the real inbound tag, not their panel's
// own tag and not a stale default.
func TestBuildUserPinsToAutoInbounds(t *testing.T) {
	m := &Manager{
		// Simulates a config pushed from the operator's panel with a custom tag,
		// after startCoreLocked derived the proxy tags from it.
		autoInbounds: []string{"vless-reality"},
	}

	// The customer's panel sends the user with its own inbound tag, which does
	// not exist on the node.
	in := &common.User{
		Email:    "bob",
		Proxies:  &common.Proxy{Vless: &common.Vless{Id: "id"}},
		Inbounds: []string{"customer-panel-inbound"},
	}
	out := m.buildUser("t1", in)
	if !slices.Equal(out.GetInbounds(), []string{"vless-reality"}) {
		t.Fatalf("user should be pinned to the real inbound, got %v", out.GetInbounds())
	}
	if out.GetEmail() != "tt1.bob" {
		t.Fatalf("email should be namespaced, got %q", out.GetEmail())
	}
}

// TestBuildUserForceOverridesAuto verifies the operator override wins over the
// auto-derived tags.
func TestBuildUserForceOverridesAuto(t *testing.T) {
	m := &Manager{
		forceInbounds: []string{"pinned"},
		autoInbounds:  []string{"vless-reality", "trojan-in"},
	}
	in := &common.User{Email: "c", Inbounds: []string{"whatever"}}
	out := m.buildUser("t1", in)
	if !slices.Equal(out.GetInbounds(), []string{"pinned"}) {
		t.Fatalf("force override should win, got %v", out.GetInbounds())
	}
}
