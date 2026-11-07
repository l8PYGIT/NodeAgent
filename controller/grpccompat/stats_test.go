package grpccompat

import (
	"context"
	"testing"

	"github.com/pasarguard/node/common"
)

// trafficStat builds a stat in the shape the xray backend produces: Type is the
// direction (uplink/downlink) and Link is always "traffic".
func trafficStat(name, direction string, value int64) *common.Stat {
	return &common.Stat{Name: name, Link: "traffic", Type: direction, Value: value}
}

func statsOf(s *common.StatResponse) map[string]int64 {
	out := make(map[string]int64, len(s.GetStats()))
	for _, st := range s.GetStats() {
		out[st.GetName()+"|"+st.GetType()] = st.GetValue()
	}
	return out
}

// A panel that connects to a tenant which already carried traffic must not
// receive that history as one delta: the first reset call seeds and reports 0.
func TestDeltaStatsSeedsInsteadOfImportingHistory(t *testing.T) {
	s := &Server{}
	const tb = int64(1) << 40

	first := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", tb),
		trafficStat("alice", "downlink", tb),
	}}))
	for key, v := range first {
		if v != 0 {
			t.Fatalf("seed report %s = %d, want 0", key, v)
		}
	}

	// One or two more MiB per direction is the only new traffic; the panel must
	// add exactly that, not the lifetime total, and each direction separately.
	second := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", tb+(1<<20)),
		trafficStat("alice", "downlink", tb+(2<<20)),
	}}))
	if got := second["alice|uplink"]; got != 1<<20 {
		t.Fatalf("uplink delta = %d, want %d", got, 1<<20)
	}
	if got := second["alice|downlink"]; got != 2<<20 {
		t.Fatalf("downlink delta = %d, want %d", got, 2<<20)
	}

	// No new traffic -> no delta.
	third := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", tb+(1<<20)),
		trafficStat("alice", "downlink", tb+(2<<20)),
	}}))
	if third["alice|uplink"] != 0 || third["alice|downlink"] != 0 {
		t.Fatalf("idle poll produced deltas: %+v", third)
	}
}

// A counter first seen after the seed can only hold traffic since the user was
// added, so its full value is new and must be reported.
func TestDeltaStatsNewUserAfterSeed(t *testing.T) {
	s := &Server{}
	s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", 100),
	}})

	got := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", 100),
		trafficStat("bob", "uplink", 4096),
	}}))
	if got["bob|uplink"] != 4096 {
		t.Fatalf("new user delta = %d, want 4096", got["bob|uplink"])
	}
	if got["alice|uplink"] != 0 {
		t.Fatalf("existing user delta = %d, want 0", got["alice|uplink"])
	}
}

// A counter dropping below its baseline means it restarted (user re-created);
// only the post-restart value is new traffic.
func TestDeltaStatsCounterRestart(t *testing.T) {
	s := &Server{}
	s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", 500),
	}})

	got := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", 30),
	}}))
	if got["alice|uplink"] != 30 {
		t.Fatalf("after restart = %d, want 30", got["alice|uplink"])
	}
}

// Baselines are per tenant: one customer's counters must never shift another's.
func TestDeltaStatsTenantIsolation(t *testing.T) {
	s := &Server{}
	s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{trafficStat("alice", "uplink", 100)}})
	s.deltaStats("t2", &common.StatResponse{Stats: []*common.Stat{trafficStat("alice", "uplink", 1000)}})

	got1 := statsOf(s.deltaStats("t1", &common.StatResponse{Stats: []*common.Stat{trafficStat("alice", "uplink", 150)}}))
	got2 := statsOf(s.deltaStats("t2", &common.StatResponse{Stats: []*common.Stat{trafficStat("alice", "uplink", 1200)}}))
	if got1["alice|uplink"] != 50 || got2["alice|uplink"] != 200 {
		t.Fatalf("tenant deltas leaked: t1=%d t2=%d", got1["alice|uplink"], got2["alice|uplink"])
	}
}

// The core key must not see tenant usage, and node-wide stat types cannot be
// attributed to one tenant, so both answer empty instead of someone else's data.
func TestGetStatsScopeAndTypeFiltering(t *testing.T) {
	s := &Server{}

	coreCtx := context.WithValue(context.Background(), ctxKey{}, callerInfo{scope: scopeCore})
	if resp, err := s.GetStats(coreCtx, &common.StatRequest{Type: common.StatType_UsersStat}); err != nil || len(resp.GetStats()) != 0 {
		t.Fatalf("core scope = %+v, %v; want empty", resp, err)
	}

	tenantCtx := context.WithValue(context.Background(), ctxKey{}, callerInfo{scope: scopeTenant, tenantID: "t1"})
	if resp, err := s.GetStats(tenantCtx, &common.StatRequest{Type: common.StatType_Outbounds}); err != nil || len(resp.GetStats()) != 0 {
		t.Fatalf("outbound stats = %+v, %v; want empty", resp, err)
	}
}

func TestFilterStatsByName(t *testing.T) {
	resp := &common.StatResponse{Stats: []*common.Stat{
		trafficStat("alice", "uplink", 1),
		trafficStat("bob", "uplink", 2),
		trafficStat("alice", "downlink", 3),
	}}
	got := filterStatsByName(resp, "alice")
	if len(got.GetStats()) != 2 {
		t.Fatalf("filtered stats = %d, want 2", len(got.GetStats()))
	}
	for _, st := range got.GetStats() {
		if st.GetName() != "alice" {
			t.Fatalf("kept unrelated stat %q", st.GetName())
		}
	}
}
