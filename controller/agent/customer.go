package agent

import (
	"errors"
	"net/http"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/shared"
)

// proxyInput is the subset of proxy credentials a customer may set for a user.
type proxyInput struct {
	VlessID      string `json:"vless_id,omitempty"`
	VlessFlow    string `json:"vless_flow,omitempty"`
	VmessID      string `json:"vmess_id,omitempty"`
	TrojanPass   string `json:"trojan_password,omitempty"`
	SSPassword   string `json:"ss_password,omitempty"`
	SSMethod     string `json:"ss_method,omitempty"`
	HysteriaAuth string `json:"hysteria_auth,omitempty"`
}

type userInput struct {
	Email    string     `json:"email"`
	Inbounds []string   `json:"inbounds"`
	Proxy    proxyInput `json:"proxy"`
}

type syncUsersRequest struct {
	Users []userInput `json:"users"`
}

// syncUsers replaces the authenticated tenant's user set. Emails are namespaced
// per tenant by the manager; the customer never controls core settings.
func (s *Server) syncUsers(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromContext(r.Context())

	var req syncUsersRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	users := make([]*common.User, 0, len(req.Users))
	for _, u := range req.Users {
		if u.Email == "" {
			writeError(w, http.StatusBadRequest, "each user requires an email")
			return
		}
		users = append(users, toCommonUser(u))
	}

	if err := s.mgr.SetTenantUsers(r.Context(), tenantID, users); err != nil {
		if errors.Is(err, shared.ErrUserLimitExceeded) {
			writeError(w, http.StatusBadRequest, "user limit exceeded")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": len(users)})
}

// myUsage returns the authenticated tenant's quota/usage.
func (s *Server) myUsage(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromContext(r.Context())
	tn, ok := s.reg.Get(tenantID)
	if !ok {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}
	uv := usageView(tn)
	if s.mgr != nil {
		uv.ActiveUsers = s.mgr.TenantUserCount(tenantID)
	}
	writeJSON(w, http.StatusOK, uv)
}

func toCommonUser(u userInput) *common.User {
	proxy := &common.Proxy{}
	if u.Proxy.VlessID != "" {
		proxy.Vless = &common.Vless{Id: u.Proxy.VlessID, Flow: u.Proxy.VlessFlow}
	}
	if u.Proxy.VmessID != "" {
		proxy.Vmess = &common.Vmess{Id: u.Proxy.VmessID}
	}
	if u.Proxy.TrojanPass != "" {
		proxy.Trojan = &common.Trojan{Password: u.Proxy.TrojanPass}
	}
	if u.Proxy.SSPassword != "" {
		proxy.Shadowsocks = &common.Shadowsocks{Password: u.Proxy.SSPassword, Method: u.Proxy.SSMethod}
	}
	if u.Proxy.HysteriaAuth != "" {
		proxy.Hysteria = &common.Hysteria{Auth: u.Proxy.HysteriaAuth}
	}
	return &common.User{
		Email:    u.Email,
		Proxies:  proxy,
		Inbounds: u.Inbounds,
	}
}
