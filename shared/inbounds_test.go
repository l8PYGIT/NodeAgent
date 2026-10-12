package shared

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pasarguard/node/tenant"
)

func TestSharableInbounds(t *testing.T) {
	cfg := `{
		"inbounds": [
			{"tag":"vless-in","port":443,"protocol":"vless"},
			{"tag":"admin-in","port":10085,"protocol":"dokodemo-door"}
		],
		"outbounds": [{"tag":"direct","protocol":"freedom"}]
	}`

	// No force-inbounds: return all inbounds, drop outbounds.
	m := &Manager{config: cfg}
	out, err := m.SharableInbounds()
	if err != nil {
		t.Fatalf("SharableInbounds: %v", err)
	}
	var doc struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Inbounds) != 2 {
		t.Fatalf("want 2 inbounds, got %d", len(doc.Inbounds))
	}
	if doc.Outbounds != nil {
		t.Fatalf("outbounds must not be shared")
	}

	// Force-inbounds narrows to the matching tag only.
	m2 := &Manager{config: cfg, forceInbounds: []string{"vless-in"}}
	out2, err := m2.SharableInbounds()
	if err != nil {
		t.Fatalf("SharableInbounds(forced): %v", err)
	}
	_ = json.Unmarshal([]byte(out2), &doc)
	if len(doc.Inbounds) != 1 || doc.Inbounds[0]["tag"] != "vless-in" {
		t.Fatalf("force-inbounds did not narrow correctly: %s", out2)
	}

	// Empty config yields an empty inbounds doc, not an error.
	m3 := &Manager{}
	if out3, err := m3.SharableInbounds(); err != nil || out3 != `{"inbounds":[]}` {
		t.Fatalf("empty config: got %q err %v", out3, err)
	}
}

func TestSharableInboundsRedactsRealityPrivateKey(t *testing.T) {
	// A valid X25519 private key (base64 raw-url) and its expected public key.
	priv := "WMHfHKQjPbTNbeKr0PmCvFjyhZjUbqI0c5q3oRZ7Ykg"
	wantPub, ok := realityPublicKey(priv)
	if !ok {
		t.Fatal("could not derive public key from test private key")
	}

	cfg := `{"inbounds":[{"tag":"vless-in","port":443,"protocol":"vless",
		"streamSettings":{"network":"tcp","security":"reality",
		"realitySettings":{"serverNames":["www.microsoft.com"],"privateKey":"` + priv + `","shortIds":["ab"]}}}]}`

	m := &Manager{config: cfg}
	out, err := m.SharableInbounds()
	if err != nil {
		t.Fatalf("SharableInbounds: %v", err)
	}
	if strings.Contains(out, priv) {
		t.Fatalf("private key leaked in shared inbounds: %s", out)
	}
	if !strings.Contains(out, wantPub) {
		t.Fatalf("expected derived public key %q in output: %s", wantPub, out)
	}
}

func TestSharableInboundsForTenant(t *testing.T) {
	cfg := `{
		"inbounds": [
			{"tag":"vless-in","port":443,"protocol":"vless"},
			{"tag":"vmess-in","port":8443,"protocol":"vmess"},
			{"tag":"admin-in","port":10085,"protocol":"dokodemo-door"}
		],
		"outbounds": [{"tag":"direct","protocol":"freedom"}]
	}`
	reg, err := tenant.NewRegistry(tenant.NewMemStore())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Tenant with AllowedInbounds = ["vless-in"]
	if _, err := reg.CreateTenant(tenant.CreateParams{
		ID:              "t1",
		APIKey:          "key-1",
		AllowedInbounds: []string{"vless-in"},
	}); err != nil {
		t.Fatal(err)
	}
	// Tenant with AllowedInbounds = ["*"]
	if _, err := reg.CreateTenant(tenant.CreateParams{
		ID:              "t2",
		APIKey:          "key-2",
		AllowedInbounds: []string{"*"},
	}); err != nil {
		t.Fatal(err)
	}

	m := &Manager{config: cfg, reg: reg, autoInbounds: []string{"vless-in", "vmess-in"}}

	// t1 filtered to vless-in only
	out1, err := m.SharableInboundsForTenant("t1")
	if err != nil {
		t.Fatalf("SharableInboundsForTenant(t1): %v", err)
	}
	var doc1 struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(out1), &doc1); err != nil {
		t.Fatal(err)
	}
	if len(doc1.Inbounds) != 1 || doc1.Inbounds[0]["tag"] != "vless-in" {
		t.Fatalf("expected only vless-in for t1, got: %s", out1)
	}

	// t2 with "*" sees all proxy inbounds (vless-in and vmess-in)
	out2, err := m.SharableInboundsForTenant("t2")
	if err != nil {
		t.Fatalf("SharableInboundsForTenant(t2): %v", err)
	}
	var doc2 struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(out2), &doc2); err != nil {
		t.Fatal(err)
	}
	if len(doc2.Inbounds) != 2 {
		t.Fatalf("expected 2 proxy inbounds for t2, got: %s", out2)
	}
}
