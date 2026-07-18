// Command agent runs the multi-tenant node agent: a single shared Xray core
// with a fixed operator config, two-level HTTP control API (master + tenant),
// and a background quota/expiry enforcement loop.
//
// Environment:
//
//	PG_AGENT_HTTP_ADDR        listen address (default :8090)
//	PG_AGENT_MASTER_KEY       master key for the main panel (required)
//	PG_AGENT_TENANT_DB        path to the tenant bbolt store (default tenants.bolt)
//	PG_AGENT_FIXED_CONFIG     path to the fixed Xray config JSON (optional; core
//	                          can also be started later via POST /admin/config)
//	PG_AGENT_ENFORCE_INTERVAL enforcement interval, e.g. 10s (default 10s)
//	PG_AGENT_CORE_KEY         optional: enables a PasarGuard-compatible gRPC API
//	                          (core-config management only) authenticated by this key
//	PG_AGENT_GRPC_ADDR        gRPC listen address for the compat API (default :62050)
//	PG_AGENT_FORCE_INBOUNDS   optional CSV override of inbound tags every tenant
//	                          user is applied to. Empty = auto-derive the proxy
//	                          inbound tags from the running core config.
//	SSL_CERT_FILE             node TLS cert (auto-generated self-signed if missing)
//	SSL_KEY_FILE              node TLS key
//	XRAY_EXECUTABLE_PATH      xray binary (consumed by config.Load)
//	XRAY_ASSETS_PATH          xray assets dir
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pasarguard/node/config"
	"github.com/pasarguard/node/controller/agent"
	"github.com/pasarguard/node/controller/grpccompat"
	"github.com/pasarguard/node/pkg/tlsutil"
	"github.com/pasarguard/node/shared"
	"github.com/pasarguard/node/tenant"
)

func main() {
	httpAddr := env("PG_AGENT_HTTP_ADDR", ":8090")
	masterKey := os.Getenv("PG_AGENT_MASTER_KEY")
	dbPath := env("PG_AGENT_TENANT_DB", "tenants.bolt")
	fixedConfigPath := os.Getenv("PG_AGENT_FIXED_CONFIG")
	enforceInterval := envDuration("PG_AGENT_ENFORCE_INTERVAL", 10*time.Second)
	coreKey := os.Getenv("PG_AGENT_CORE_KEY")
	grpcAddr := env("PG_AGENT_GRPC_ADDR", ":62050")
	forceInbounds := splitCSV(os.Getenv("PG_AGENT_FORCE_INBOUNDS"))

	if masterKey == "" {
		log.Fatal("PG_AGENT_MASTER_KEY is required")
	}

	nodeCfg, err := config.Load()
	if err != nil {
		log.Fatalf("load node config: %v", err)
	}

	store, err := tenant.OpenBoltStore(dbPath)
	if err != nil {
		log.Fatalf("open tenant store: %v", err)
	}
	defer store.Close()

	reg, err := tenant.NewRegistry(store)
	if err != nil {
		log.Fatalf("build registry: %v", err)
	}

	mgr := shared.NewManager(nodeCfg, reg)
	mgr.SetForceInbounds(forceInbounds)
	authn := tenant.NewAuthenticator(masterKey, reg)
	srv := agent.NewServer(reg, mgr, authn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Optionally bring up the shared core from a fixed config file at startup.
	if fixedConfigPath != "" {
		data, err := os.ReadFile(fixedConfigPath)
		if err != nil {
			log.Fatalf("read fixed config: %v", err)
		}
		if err := mgr.ApplyConfig(ctx, string(data)); err != nil {
			log.Fatalf("apply fixed config: %v", err)
		}
		log.Printf("shared core started from %s (version %s)", fixedConfigPath, mgr.Version())
	} else {
		log.Println("no fixed config provided; core can be started via POST /admin/config")
	}

	go srv.RunEnforcement(ctx, enforceInterval)

	// Self-signed TLS: generate on first run; the main panel pins this cert when
	// registering the node.
	if err := tlsutil.EnsureSelfSigned(nodeCfg.SslCertFile, nodeCfg.SslKeyFile); err != nil {
		log.Fatalf("ensure tls cert: %v", err)
	}
	if certPEM, err := os.ReadFile(nodeCfg.SslCertFile); err == nil {
		log.Printf("node TLS certificate (register this in the main panel):\n%s", certPEM)
	}

	// PasarGuard-compatible gRPC API. Always started: customers connect here with
	// their tenant keys to provision users. When PG_AGENT_CORE_KEY is set, the
	// operator can also connect their own PasarGuard panel with the core key to
	// manage ONLY the shared Xray core config (user/Stop ops from it are ignored).
	tlsConfig, err := tlsutil.LoadTLSCredentials(nodeCfg.SslCertFile, nodeCfg.SslKeyFile)
	if err != nil {
		log.Fatalf("load tls for grpc: %v", err)
	}
	stopGRPC, err := grpccompat.Serve(tlsConfig, grpcAddr, grpccompat.New(mgr, authn, coreKey))
	if err != nil {
		log.Fatalf("start grpc compat: %v", err)
	}
	if coreKey == "" {
		log.Println("PG_AGENT_CORE_KEY not set; core-config management via PasarGuard disabled (customer provisioning still works)")
	}

	httpSrv := &http.Server{
		Addr:              httpAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("agent listening on %s (https)", httpAddr)
		if err := httpSrv.ListenAndServeTLS(nodeCfg.SslCertFile, nodeCfg.SslKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down agent...")
	cancel()
	if stopGRPC != nil {
		stopGRPC()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	mgr.Stop()
	log.Println("agent stopped")
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
