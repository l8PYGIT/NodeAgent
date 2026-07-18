package grpccompat

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/pkg/netutil"
	"github.com/pasarguard/node/shared"
	"github.com/pasarguard/node/tenant"
)

// startBufServer runs the compat gRPC server over an in-memory listener (no TLS)
// so we can test the semantics without certificates.
func startBufServer(t *testing.T, srv *Server) common.NodeServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer(
		grpc.UnaryInterceptor(srv.unaryAuth),
		grpc.StreamInterceptor(srv.streamAuth),
	)
	common.RegisterNodeServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return common.NewNodeServiceClient(conn)
}

func authCtx(key string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-api-key", key))
}

func TestGRPCAuth(t *testing.T) {
	reg, _ := tenant.NewRegistry(tenant.NewMemStore())
	authn := tenant.NewAuthenticator("master", reg)
	client := startBufServer(t, New(shared.NewManager(&config.Config{}, reg), authn, "core-secret"))

	// Wrong key -> denied.
	if _, err := client.GetBaseInfo(authCtx("wrong"), &common.Empty{}); err == nil {
		t.Fatal("expected auth failure for wrong core key")
	}
	// Correct key -> ok.
	if _, err := client.GetBaseInfo(authCtx("core-secret"), &common.Empty{}); err != nil {
		t.Fatalf("expected success with correct core key, got %v", err)
	}
}

func TestGRPCSyncUsersIsNoop(t *testing.T) {
	reg, _ := tenant.NewRegistry(tenant.NewMemStore())
	mgr := shared.NewManager(&config.Config{}, reg)
	authn := tenant.NewAuthenticator("master", reg)
	client := startBufServer(t, New(mgr, authn, "core-secret"))

	ctx := authCtx("core-secret")

	// SyncUsers from the core operator must be accepted (no error)...
	if _, err := client.SyncUsers(ctx, &common.Users{Users: []*common.User{
		{Email: "panel-user", Proxies: &common.Proxy{Vless: &common.Vless{Id: "x"}}, Inbounds: []string{"vless-in"}},
	}}); err != nil {
		t.Fatalf("SyncUsers should be accepted, got %v", err)
	}
	// ...but must NOT create any tenant or apply users in our system.
	if got := len(reg.List()); got != 0 {
		t.Fatalf("core-scope SyncUsers must not create tenants/users, found %d", got)
	}

	// Stop must be a no-op (returns ok, does nothing).
	if _, err := client.Stop(ctx, &common.Empty{}); err != nil {
		t.Fatalf("Stop should be a no-op success, got %v", err)
	}
}

// TestGRPCGetLogsStaysOpen verifies the log stream blocks until the client
// cancels instead of returning EOF immediately. A PasarGuard panel treats an
// immediate EOF on GetLogs as the node connection dropping, which triggers a
// reconnect loop and "EOF" errors against the customer's users.
func TestGRPCGetLogsStaysOpen(t *testing.T) {
	reg, _ := tenant.NewRegistry(tenant.NewMemStore())
	authn := tenant.NewAuthenticator("master", reg)
	client := startBufServer(t, New(shared.NewManager(&config.Config{}, reg), authn, "core-secret"))

	ctx, cancel := context.WithCancel(authCtx("core-secret"))
	stream, err := client.GetLogs(ctx, &common.Empty{})
	if err != nil {
		t.Fatalf("GetLogs open failed: %v", err)
	}

	// The stream must not close on its own; Recv should block until we cancel.
	recvErr := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErr <- err
	}()

	select {
	case err := <-recvErr:
		t.Fatalf("GetLogs closed on its own (got %v); it must stay open until the client cancels", err)
	case <-time.After(200 * time.Millisecond):
		// Good: still open.
	}

	cancel()
	select {
	case <-recvErr: // stream closed after we cancelled — expected
	case <-time.After(2 * time.Second):
		t.Fatal("GetLogs did not close after client cancellation")
	}
}

func TestGRPCTenantKeyProvisionsUsers(t *testing.T) {
	reg, _ := tenant.NewRegistry(tenant.NewMemStore())
	if _, err := reg.CreateTenant(tenant.CreateParams{ID: "t1", APIKey: "cust-key", QuotaBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	mgr := shared.NewManager(&config.Config{}, reg) // core nil: users tracked, not applied to xray
	authn := tenant.NewAuthenticator("master", reg)
	client := startBufServer(t, New(mgr, authn, "core-secret"))

	// A customer's panel connects with its key and Start()s with its users.
	if _, err := client.Start(authCtx("cust-key"), &common.Backend{
		Type:   common.BackendType_XRAY,
		Config: `{"inbounds":[]}`, // ignored for tenant scope
		Users: []*common.User{
			{Email: "alice", Proxies: &common.Proxy{Vless: &common.Vless{Id: "11111111-1111-1111-1111-111111111111"}}, Inbounds: []string{"vless-in"}},
		},
	}); err != nil {
		t.Fatalf("tenant Start should succeed, got %v", err)
	}
	if got := mgr.TenantUserCount("t1"); got != 1 {
		t.Fatalf("expected 1 user provisioned for t1, got %d", got)
	}

	// An unknown key is rejected.
	if _, err := client.Start(authCtx("bogus"), &common.Backend{Type: common.BackendType_XRAY}); err == nil {
		t.Fatal("unknown key should be rejected")
	}
}

// TestGRPCStartAppliesConfig requires a real xray binary; it proves the panel
// can set the shared core config over gRPC.
func TestGRPCStartAppliesConfig(t *testing.T) {
	exe := os.Getenv("XRAY_EXECUTABLE_PATH")
	if exe == "" {
		t.Skip("set XRAY_EXECUTABLE_PATH to run the gRPC Start integration test")
	}
	cfg := &config.Config{
		XrayExecutablePath:  exe,
		XrayAssetsPath:      os.Getenv("XRAY_ASSETS_PATH"),
		GeneratedConfigPath: t.TempDir(),
		LogBufferSize:       10000,
		StartupLogTailSize:  200,
	}
	reg, _ := tenant.NewRegistry(tenant.NewMemStore())
	mgr := shared.NewManager(cfg, reg)
	defer mgr.Stop()
	client := startBufServer(t, New(mgr, tenant.NewAuthenticator("master", reg), "core-secret"))

	port := netutil.FindFreePort()
	xrayCfg := fmt.Sprintf(`{"log":{"loglevel":"warning"},"inbounds":[{"tag":"vless-in","listen":"127.0.0.1","port":%d,"protocol":"vless","settings":{"clients":[],"decryption":"none"}}],"outbounds":[{"tag":"direct","protocol":"freedom"}]}`, port)

	ctx, cancel := context.WithTimeout(authCtx("core-secret"), 30*time.Second)
	defer cancel()

	resp, err := client.Start(ctx, &common.Backend{Type: common.BackendType_XRAY, Config: xrayCfg})
	if err != nil {
		t.Fatalf("Start over gRPC failed: %v", err)
	}
	if !resp.GetStarted() {
		t.Fatalf("core should be started, got %+v", resp)
	}
	if !mgr.Started() {
		t.Fatal("manager core should be running after gRPC Start")
	}
}
