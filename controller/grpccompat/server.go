// Package grpccompat exposes a PasarGuard-compatible gRPC NodeService so an
// external panel (e.g. PasarGuard) can manage ONLY the node's shared core
// config, while the multi-tenant user/quota state remains owned by our HTTP
// control plane.
//
// Rationale: the external panel does not know this node is "sold" (multi-tenant).
// So this server:
//   - accepts Start(config) and applies the config to the shared core;
//   - IGNORES the users the panel sends (SyncUser/SyncUsers are accepted but are
//     no-ops) so the panel cannot clobber tenant users;
//   - makes Stop a no-op so the panel cannot take the sold node down;
//   - returns empty (not errors) for stats/logs so the panel UI stays happy.
//
// It is gated by a dedicated core key and only started when that key is set.
package grpccompat

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"io"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/controller"
	"github.com/pasarguard/node/pkg/sysstats"
	"github.com/pasarguard/node/shared"
	"github.com/pasarguard/node/tenant"
)

const nodeVersion = "0.5.2-mt"

// Server implements common.NodeServiceServer with sandboxed semantics.
//
// Two kinds of caller are accepted:
//   - core key  -> manages ONLY the shared core config (Start applies config,
//     user ops are ignored). For the operator / a PasarGuard panel used to drive
//     the node's core like a normal node.
//   - customer key -> a tenant. Start/SyncUser(s) provision THAT tenant's users
//     (quota-enforced), config is ignored, stats are scoped to the tenant. This
//     lets a customer's own PasarGuard panel manage its users directly.
type Server struct {
	common.UnimplementedNodeServiceServer
	mgr     *shared.Manager
	authn   *tenant.Authenticator
	coreKey string
	// shutdown is closed when the server is stopping, so deliberately blocking
	// stream handlers can return instead of holding GracefulStop forever.
	shutdown chan struct{}
}

func New(mgr *shared.Manager, authn *tenant.Authenticator, coreKey string) *Server {
	return &Server{mgr: mgr, authn: authn, coreKey: coreKey, shutdown: make(chan struct{})}
}

type scopeKind int

const (
	scopeCore scopeKind = iota
	scopeTenant
)

type ctxKey struct{}

type callerInfo struct {
	scope    scopeKind
	tenantID string
}

// Serve starts the gRPC server on addr with the given TLS config until the
// listener is closed. Returns a stop function.
func Serve(tlsConfig *tls.Config, addr string, srv *Server) (func(), error) {
	const maxMsg = 64 * 1024 * 1024
	gs := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsConfig)),
		grpc.MaxRecvMsgSize(maxMsg),
		grpc.MaxSendMsgSize(maxMsg),
		grpc.UnaryInterceptor(srv.unaryAuth),
		grpc.StreamInterceptor(srv.streamAuth),
	)
	common.RegisterNodeServiceServer(gs, srv)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		log.Printf("gRPC (PasarGuard-compat) listening on %s", addr)
		if err := gs.Serve(ln); err != nil {
			log.Printf("grpc compat server stopped: %v", err)
		}
	}()

	// Shut down with a bounded wait.
	//
	// GracefulStop blocks until every in-flight RPC returns and does NOT cancel
	// their contexts. GetLogs deliberately blocks until its client goes away, and
	// a panel normally holds it open — so a bare GracefulStop never returned:
	// SIGTERM hung the agent until systemd's 90s timeout SIGKILLed it, skipping
	// every deferred cleanup and stalling each restart, including the one the
	// self-update performs.
	stop := func() {
		// Tell streaming handlers to wind up, so they can return before the
		// forced stop below.
		close(srv.shutdown)
		done := make(chan struct{})
		go func() {
			gs.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(gracefulStopTimeout):
			log.Printf("grpc compat server: graceful stop timed out after %s; forcing", gracefulStopTimeout)
			gs.Stop()
		}
	}
	return stop, nil
}

// gracefulStopTimeout bounds how long shutdown waits for in-flight RPCs.
const gracefulStopTimeout = 5 * time.Second

// --- auth ---

// resolveCaller authenticates the x-api-key and classifies the caller as the
// core operator or a specific tenant.
func (s *Server) resolveCaller(ctx context.Context) (callerInfo, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return callerInfo{}, status.Error(codes.Unauthenticated, "missing metadata")
	}
	keys := md.Get("x-api-key")
	if len(keys) == 0 || keys[0] == "" {
		return callerInfo{}, status.Error(codes.Unauthenticated, "missing x-api-key")
	}
	key := keys[0]

	if s.coreKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(s.coreKey)) == 1 {
		return callerInfo{scope: scopeCore}, nil
	}

	// Otherwise treat it as a tenant (customer) key.
	if s.authn == nil {
		return callerInfo{}, status.Error(codes.PermissionDenied, "invalid key")
	}
	scope, decision := s.authn.Authenticate(key, time.Now().Unix())
	if scope != tenant.ScopeTenant || !decision.Allowed || decision.Tenant == nil {
		switch decision.Code {
		case tenant.CodeResourceExhausted:
			return callerInfo{}, status.Error(codes.ResourceExhausted, "quota exhausted")
		case tenant.CodePermissionDenied:
			return callerInfo{}, status.Error(codes.PermissionDenied, "access denied")
		default:
			return callerInfo{}, status.Error(codes.Unauthenticated, "invalid key")
		}
	}
	return callerInfo{scope: scopeTenant, tenantID: decision.Tenant.ID}, nil
}

func (s *Server) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ci, err := s.resolveCaller(ctx)
	if err != nil {
		return nil, err
	}
	return handler(context.WithValue(ctx, ctxKey{}, ci), req)
}

func (s *Server) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ci, err := s.resolveCaller(ss.Context())
	if err != nil {
		return err
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), ctxKey{}, ci)})
}

func caller(ctx context.Context) callerInfo {
	ci, _ := ctx.Value(ctxKey{}).(callerInfo)
	return ci
}

// wrappedStream lets us attach the caller info to a stream's context.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// --- core management (honored) ---

// Start: core scope applies the config; tenant scope provisions the tenant's
// users (config ignored). Always returns success so the node shows connected.
func (s *Server) Start(ctx context.Context, b *common.Backend) (*common.BaseInfoResponse, error) {
	ci := caller(ctx)
	if ci.scope == scopeTenant {
		if err := s.mgr.SetTenantUsers(ctx, ci.tenantID, b.GetUsers()); err != nil {
			return nil, status.Errorf(codes.Internal, "apply users: %v", err)
		}
		return s.baseInfo(), nil
	}
	// core scope
	if b.GetType() != common.BackendType_XRAY {
		return nil, status.Error(codes.InvalidArgument, "only xray backend is supported")
	}
	if cfg := b.GetConfig(); cfg != "" {
		if err := s.mgr.ApplyConfig(ctx, cfg); err != nil {
			return nil, status.Errorf(codes.Internal, "apply config: %v", err)
		}
	}
	return s.baseInfo(), nil
}

func (s *Server) GetBaseInfo(context.Context, *common.Empty) (*common.BaseInfoResponse, error) {
	return s.baseInfo(), nil
}

func (s *Server) baseInfo() *common.BaseInfoResponse {
	return &common.BaseInfoResponse{
		Started:     s.mgr.Started(),
		CoreVersion: s.mgr.Version(),
		NodeVersion: nodeVersion,
	}
}

func (s *Server) GetSystemStats(context.Context, *common.Empty) (*common.SystemStatsResponse, error) {
	stats, err := sysstats.GetSystemStats()
	if err != nil {
		return &common.SystemStatsResponse{}, nil
	}
	return stats, nil
}

func (s *Server) GetBackendStats(context.Context, *common.Empty) (*common.BackendStatsResponse, error) {
	return &common.BackendStatsResponse{}, nil
}

// --- user operations (accepted but NOT applied) ---

func (s *Server) Stop(context.Context, *common.Empty) (*common.Empty, error) {
	// No-op: neither an external panel nor a customer may stop a sold node.
	return &common.Empty{}, nil
}

// SyncUsers replaces the tenant's user set (tenant scope); ignored for core.
func (s *Server) SyncUsers(ctx context.Context, u *common.Users) (*common.Empty, error) {
	ci := caller(ctx)
	if ci.scope == scopeTenant {
		if err := s.mgr.SetTenantUsers(ctx, ci.tenantID, u.GetUsers()); err != nil {
			return nil, status.Errorf(codes.Internal, "apply users: %v", err)
		}
	}
	return &common.Empty{}, nil
}

// SyncUser streams individual user updates; for a tenant they are merged in.
//
// Each user is applied AS IT ARRIVES. A customer's panel keeps this stream open
// and pushes incremental updates over it, so buffering until io.EOF meant a
// newly created user did not exist on the node until the stream happened to
// close — their client just got EOF in the meantime — and a stream that died
// with any non-EOF error silently discarded everything it had buffered.
func (s *Server) SyncUser(stream grpc.ClientStreamingServer[common.User, common.Empty]) error {
	ci := caller(stream.Context())
	for {
		u, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return stream.SendAndClose(&common.Empty{})
			}
			return err
		}
		if ci.scope != scopeTenant || u == nil {
			continue
		}
		if e := s.mgr.AddTenantUsers(stream.Context(), ci.tenantID, []*common.User{u}); e != nil {
			return status.Errorf(codes.Internal, "apply user: %v", e)
		}
	}
}

// SyncUsersChunked accumulates all chunks and replaces the tenant's user set.
//
// The replace is destructive, so the chunk sequence must be proven complete
// first: chunks are keyed by index and validated by BuildUsersFromChunks, which
// rejects a missing final marker, gaps in the indices, and an empty result.
// Without that, a stream truncated after chunk 0 (a client-side abort that
// still closes cleanly) was treated as the tenant's entire user set and
// silently removed everyone who was not in it.
func (s *Server) SyncUsersChunked(stream grpc.ClientStreamingServer[common.UsersChunk, common.Empty]) error {
	ci := caller(stream.Context())
	chunks := make(map[uint64][]*common.User)
	var lastIndex uint64
	var sawLast bool
	for {
		c, err := stream.Recv()
		if err != nil {
			if err != io.EOF {
				return err
			}
			if ci.scope != scopeTenant {
				return stream.SendAndClose(&common.Empty{})
			}
			users, buildErr := controller.BuildUsersFromChunks(chunks, lastIndex, sawLast)
			if buildErr != nil {
				return status.Errorf(codes.InvalidArgument, "incomplete user chunks: %v", buildErr)
			}
			if e := s.mgr.SetTenantUsers(stream.Context(), ci.tenantID, users); e != nil {
				return status.Errorf(codes.Internal, "apply users: %v", e)
			}
			return stream.SendAndClose(&common.Empty{})
		}
		chunks[c.GetIndex()] = c.GetUsers()
		if c.GetLast() {
			lastIndex = c.GetIndex()
			sawLast = true
		}
	}
}

// --- stats/logs (empty, not errors, to keep the panel UI happy) ---

// GetStats returns the tenant's per-user stats (de-namespaced) for a customer;
// nothing for the core scope (to avoid leaking other tenants' data).
func (s *Server) GetStats(ctx context.Context, _ *common.StatRequest) (*common.StatResponse, error) {
	ci := caller(ctx)
	if ci.scope == scopeTenant {
		return s.mgr.GetTenantUserStats(ctx, ci.tenantID)
	}
	return &common.StatResponse{}, nil
}

func (s *Server) GetOutboundsLatency(context.Context, *common.LatencyRequest) (*common.LatencyResponse, error) {
	return &common.LatencyResponse{}, nil
}

func (s *Server) GetUserOnlineStats(context.Context, *common.StatRequest) (*common.OnlineStatResponse, error) {
	return &common.OnlineStatResponse{}, nil
}

func (s *Server) GetUserOnlineIpListStats(context.Context, *common.StatRequest) (*common.StatsOnlineIpListResponse, error) {
	return &common.StatsOnlineIpListResponse{}, nil
}

// GetLogs holds the stream open (sending no log lines) until the client
// disconnects. Returning immediately would close the stream with io.EOF, which a
// PasarGuard panel treats as the node connection dropping — causing a reconnect
// loop and "EOF" errors against the customer's users. Blocking mirrors the real
// node's GetLogs, which streams until the client cancels.
//
// It also returns when the server is shutting down: GracefulStop waits for
// in-flight RPCs without cancelling them, so a handler that only watched the
// stream context would keep a connected panel's stream — and therefore the
// whole shutdown — open indefinitely.
func (s *Server) GetLogs(_ *common.Empty, stream grpc.ServerStreamingServer[common.Log]) error {
	select {
	case <-stream.Context().Done():
	case <-s.shutdown:
	}
	return nil
}
