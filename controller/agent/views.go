package agent

import "github.com/pasarguard/node/tenant"

// tenantViewModel is the master-facing representation of a tenant. The hashed
// API key is intentionally not exposed.
type tenantViewModel struct {
	ID               string   `json:"id"`
	Status           string   `json:"status"`
	Reason           string   `json:"reason,omitempty"`
	PeriodID         uint64   `json:"period_id"`
	QuotaBytes       int64    `json:"quota_bytes"`
	UsedBytes        int64    `json:"used_bytes"`
	CreditLimitBytes int64    `json:"credit_limit_bytes"`
	ExpireAt         int64    `json:"expire_at"`
	AllowedInbounds  []string `json:"allowed_inbounds,omitempty"`
	MaxUsers         int      `json:"max_users,omitempty"`
	CoreType         string   `json:"core_type,omitempty"`
	MaxIPs           int      `json:"max_ips,omitempty"`
	SpeedLimitMbps   int      `json:"speed_limit_mbps,omitempty"`
	Domains          []string `json:"domains,omitempty"`
	InternalPort     int      `json:"internal_port,omitempty"`
}

func tenantView(t *tenant.Tenant) tenantViewModel {
	if t == nil {
		return tenantViewModel{}
	}
	return tenantViewModel{
		ID:               t.ID,
		Status:           string(t.Status),
		Reason:           string(t.Reason),
		PeriodID:         t.PeriodID,
		QuotaBytes:       t.QuotaBytes,
		UsedBytes:        t.UsedBytes,
		CreditLimitBytes: t.CreditLimitBytes,
		ExpireAt:         t.ExpireAt,
		AllowedInbounds:  t.AllowedInbounds,
		MaxUsers:         t.MaxUsers,
		CoreType:         t.CoreType,
		MaxIPs:           t.MaxIPs,
		SpeedLimitMbps:   t.SpeedLimitMbps,
		Domains:          t.Domains,
		InternalPort:     t.InternalPort,
	}
}

// usageViewModel is returned for usage queries (cumulative absolute counters so
// the external billing system can compute deltas idempotently).
type usageViewModel struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	PeriodID     uint64 `json:"period_id"`
	QuotaBytes   int64  `json:"quota_bytes"`
	UsedBytes    int64  `json:"used_bytes"`
	OverageBytes int64  `json:"overage_bytes"`
	RemainBytes  int64  `json:"remaining_bytes"`
	CreditLimit  int64  `json:"credit_limit_bytes"`
	ExpireAt     int64  `json:"expire_at"`
	ActiveUsers  int    `json:"active_users"`
}

func usageView(t *tenant.Tenant) usageViewModel {
	overage := t.UsedBytes - t.QuotaBytes
	if overage < 0 {
		overage = 0
	}
	remaining := t.QuotaBytes - t.UsedBytes
	if remaining < 0 {
		remaining = 0
	}
	return usageViewModel{
		ID:           t.ID,
		Status:       string(t.Status),
		PeriodID:     t.PeriodID,
		QuotaBytes:   t.QuotaBytes,
		UsedBytes:    t.UsedBytes,
		OverageBytes: overage,
		RemainBytes:  remaining,
		CreditLimit:  t.CreditLimitBytes,
		ExpireAt:     t.ExpireAt,
	}
}
