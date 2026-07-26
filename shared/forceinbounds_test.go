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

// TestPinnedForCoreRePinsStaleUsers is the regression test for the OTHER half of
// the EOF bug.
//
// buildUser pins a user's inbound tags at sync time and the result is what stays
// in m.users. When the operator later pushes a config whose inbound is tagged
// differently, re-applying those STORED users would sync them onto a tag that no
// longer exists — every already-provisioned end-user matches zero inbounds and
// gets EOF. Applying must re-resolve the tags against the running config.
func TestPinnedForCoreRePinsStaleUsers(t *testing.T) {
	m := &Manager{autoInbounds: []string{"vless-in"}}

	// Provisioned while the default config was running.
	stored := m.buildUser("t1", &common.User{
		Email:   "bob",
		Proxies: &common.Proxy{Vless: &common.Vless{Id: "id"}},
	})
	if !slices.Equal(stored.GetInbounds(), []string{"vless-in"}) {
		t.Fatalf("precondition: stored user inbounds = %v, want [vless-in]", stored.GetInbounds())
	}

	// The operator pushes a config whose inbound is tagged differently.
	m.autoInbounds = []string{"vless-reality"}

	pinned := m.pinnedForCore(stored)
	if !slices.Equal(pinned.GetInbounds(), []string{"vless-reality"}) {
		t.Fatalf("re-applied user inbounds = %v, want [vless-reality] (stale tags cause EOF)", pinned.GetInbounds())
	}
	if pinned.GetEmail() != stored.GetEmail() {
		t.Fatalf("re-pinning must not change the email: %q vs %q", pinned.GetEmail(), stored.GetEmail())
	}
	if pinned.GetProxies() != stored.GetProxies() {
		t.Fatal("re-pinning must preserve the user's credentials")
	}
}

// With no effective tags at all, a user must be left alone: withInbounds(u, nil)
// means "remove from every inbound", which must never happen by accident.
func TestPinnedForCoreLeavesUserAloneWithoutTags(t *testing.T) {
	m := &Manager{}
	in := &common.User{Email: "tt1.bob", Inbounds: []string{"whatever"}}
	if got := m.pinnedForCore(in); got != in {
		t.Fatalf("user should be returned unchanged when no tags are known, got %+v", got)
	}
}

// An unchanged user is returned as-is rather than copied.
func TestPinnedForCoreNoOpWhenAlreadyCorrect(t *testing.T) {
	m := &Manager{autoInbounds: []string{"vless-reality"}}
	in := &common.User{Email: "tt1.bob", Inbounds: []string{"vless-reality"}}
	if got := m.pinnedForCore(in); got != in {
		t.Fatal("an already-correct user should not be rebuilt")
	}
}

func TestSameTags(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a", "b"}, []string{"b", "a"}, false},
		{[]string{"a"}, nil, false},
		{[]string{"a"}, []string{"a", "b"}, false},
	}
	for _, c := range cases {
		if got := sameTags(c.a, c.b); got != c.want {
			t.Errorf("sameTags(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
