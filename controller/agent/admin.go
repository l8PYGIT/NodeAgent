package agent

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/pasarguard/node/tenant"
)

// applyConfig sets the fixed shared-core config and (re)starts the core.
func (s *Server) applyConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "config body is empty")
		return
	}
	if err := s.mgr.ApplyConfig(r.Context(), string(body)); err != nil {
		writeError(w, http.StatusInternalServerError, "apply config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"core_started": s.mgr.Started(),
		"core_version": s.mgr.Version(),
	})
}

// getConfig returns the raw JSON of the currently running core config (master
// scope only — may contain operator-only outbounds/routing).
func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.mgr.Config()
	w.Header().Set("Content-Type", "application/json")
	if cfg == "" {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_, _ = w.Write([]byte(cfg))
}

// getInbounds returns the customer-shareable inbound definitions, so the panel
// can hand them to a buyer to replicate the connection in their own panel.
func (s *Server) getInbounds(w http.ResponseWriter, _ *http.Request) {
	doc, err := s.mgr.SharableInbounds()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read inbounds: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(doc))
}

// validTenantID constrains tenant ids to characters that keep user namespacing
// unambiguous.
//
// Users are stored as "t<tenantID>.<email>". With a dot allowed in the id, that
// mapping is not injective: tenant "a" with user "b.x" and tenant "a.b" with
// user "x" both produce "ta.b.x". They would then overwrite each other's user
// records and traffic attribution, and the prefix-based stats filter would let
// one tenant read the other's usage.
func validTenantID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

type createTenantRequest struct {
	ID               string   `json:"id"`
	APIKey           string   `json:"api_key"`
	QuotaBytes       int64    `json:"quota_bytes"`
	CreditLimitBytes int64    `json:"credit_limit_bytes"`
	ExpireAt         int64    `json:"expire_at"`
	PeriodID         uint64   `json:"period_id"`
	AllowedInbounds  []string `json:"allowed_inbounds"`
	MaxUsers         int      `json:"max_users"`
	CoreType         string   `json:"core_type"`
	MaxIPs           int      `json:"max_ips"`
	SpeedLimitMbps   int      `json:"speed_limit_mbps"`
	Domains          []string `json:"domains"`
	InternalPort     int      `json:"internal_port"`
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.ID == "" || req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "id and api_key are required")
		return
	}
	if !validTenantID(req.ID) {
		writeError(w, http.StatusBadRequest,
			"id must be 1-64 characters of letters, digits, '-' or '_' (no dots: they would make user namespacing ambiguous)")
		return
	}

	tn, err := s.reg.CreateTenant(tenant.CreateParams{
		ID:               req.ID,
		APIKey:           req.APIKey,
		QuotaBytes:       req.QuotaBytes,
		CreditLimitBytes: req.CreditLimitBytes,
		ExpireAt:         req.ExpireAt,
		PeriodID:         req.PeriodID,
		AllowedInbounds:  req.AllowedInbounds,
		MaxUsers:         req.MaxUsers,
		CoreType:         req.CoreType,
		MaxIPs:           req.MaxIPs,
		SpeedLimitMbps:   req.SpeedLimitMbps,
		Domains:          req.Domains,
		InternalPort:     req.InternalPort,
	})
	if err != nil {
		switch {
		case errors.Is(err, tenant.ErrAlreadyExists), errors.Is(err, tenant.ErrKeyConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, tenantView(tn))
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.DeleteTenant(r.Context(), id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setQuotaRequest struct {
	QuotaBytes       int64 `json:"quota_bytes"`
	CreditLimitBytes int64 `json:"credit_limit_bytes"`
	ExpireAt         int64 `json:"expire_at"`
}

func (s *Server) setQuota(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req setQuotaRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	tn, err := s.reg.SetQuota(id, req.QuotaBytes, req.CreditLimitBytes, req.ExpireAt)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tenantView(tn))
}

func (s *Server) suspendTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.SuspendTenant(r.Context(), id, tenant.ReasonManual); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	tn, _ := s.reg.Get(id)
	writeJSON(w, http.StatusOK, tenantView(tn))
}

func (s *Server) resumeTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.ResumeTenant(r.Context(), id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	tn, _ := s.reg.Get(id)
	writeJSON(w, http.StatusOK, tenantView(tn))
}

func (s *Server) resetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.reg.ResetPeriod(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	// Re-apply users now that the tenant is active again.
	if err := s.mgr.ResumeTenant(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tn, _ := s.reg.Get(id)
	writeJSON(w, http.StatusOK, tenantView(tn))
}

func (s *Server) listTenants(w http.ResponseWriter, _ *http.Request) {
	tenants := s.reg.List()
	views := make([]tenantViewModel, 0, len(tenants))
	for _, tn := range tenants {
		views = append(views, tenantView(tn))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) tenantUsage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tn, ok := s.reg.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	uv := usageView(tn)
	if s.mgr != nil {
		uv.ActiveUsers = s.mgr.TenantUserCount(id)
	}
	writeJSON(w, http.StatusOK, uv)
}

func writeNotFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, tenant.ErrNotFound) {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
