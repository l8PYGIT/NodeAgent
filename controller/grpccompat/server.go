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
}

func New(mgr *shared.Manager, authn *tenant.Authenticator, coreKey string) *Server {
	return &Server{mgr: mgr, authn: authn, coreKey: coreKey}
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
	return gs.GracefulStop, nil
}

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
func (s *Server) SyncUser(stream grpc.ClientStreamingServer[common.User, common.Empty]) error {
	ci := caller(stream.Context())
	var users []*common.User
	for {
		u, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				if ci.scope == scopeTenant && len(users) > 0 {
					if e := s.mgr.AddTenantUsers(stream.Context(), ci.tenantID, users); e != nil {
						return status.Errorf(codes.Internal, "apply users: %v", e)
					}
				}
				return stream.SendAndClose(&common.Empty{})
			}
			return err
		}
		users = append(users, u)
	}
}

// SyncUsersChunked accumulates all chunks and replaces the tenant's user set.
func (s *Server) SyncUsersChunked(stream grpc.ClientStreamingServer[common.UsersChunk, common.Empty]) error {
	ci := caller(stream.Context())
	var users []*common.User
	for {
		c, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				if ci.scope == scopeTenant {
					if e := s.mgr.SetTenantUsers(stream.Context(), ci.tenantID, users); e != nil {
						return status.Errorf(codes.Internal, "apply users: %v", e)
					}
				}
				return stream.SendAndClose(&common.Empty{})
			}
			return err
		}
		users = append(users, c.GetUsers()...)
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
func (s *Server) GetLogs(_ *common.Empty, stream grpc.ServerStreamingServer[common.Log]) error {
	<-stream.Context().Done()
	return nil
}
