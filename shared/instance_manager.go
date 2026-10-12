package shared

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// DedicatedInstance tracks the state and configuration of an isolated Xray core
// for a single dedicated-core tenant.
type DedicatedInstance struct {
	mu         sync.Mutex
	TenantID   string
	Port       int
	ConfigJSON string
	Started    bool
}

// Start marks the dedicated instance started with the provided configuration.
func (d *DedicatedInstance) Start(ctx context.Context, configJSON string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ConfigJSON = configJSON
	d.Started = true
	return nil
}

// Stop marks the dedicated instance stopped.
func (d *DedicatedInstance) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Started = false
	return nil
}

// ApplyConfig updates the dedicated instance configuration and ensures it is started.
func (d *DedicatedInstance) ApplyConfig(ctx context.Context, configJSON string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ConfigJSON = configJSON
	d.Started = true
	return nil
}

// InstanceManager manages dedicated tenant port assignments and dedicated instances.
type InstanceManager struct {
	mu        sync.Mutex
	basePort  int
	ports     map[string]int
	instances map[string]*DedicatedInstance
}

// NewInstanceManager returns an InstanceManager with the given basePort (defaults to 11000 if <= 0).
func NewInstanceManager(basePort int) *InstanceManager {
	if basePort <= 0 {
		basePort = 11000
	}
	return &InstanceManager{
		basePort:  basePort,
		ports:     make(map[string]int),
		instances: make(map[string]*DedicatedInstance),
	}
}

// AllocatePort allocates the next free port in range [basePort, basePort+1000) for tenantID.
// Reuses the port if already allocated for the tenant.
func (im *InstanceManager) AllocatePort(tenantID string) (int, error) {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.allocatePortLocked(tenantID)
}

func (im *InstanceManager) allocatePortLocked(tenantID string) (int, error) {
	if p, ok := im.ports[tenantID]; ok {
		return p, nil
	}

	used := make(map[int]bool, len(im.ports))
	for _, p := range im.ports {
		used[p] = true
	}

	for p := im.basePort; p < im.basePort+1000; p++ {
		if !used[p] {
			im.ports[tenantID] = p
			return p, nil
		}
	}

	return 0, fmt.Errorf("port range [%d, %d) exhausted", im.basePort, im.basePort+1000)
}

// ReleasePort releases the port allocated to tenantID and cleans up any existing instance.
func (im *InstanceManager) ReleasePort(tenantID string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	delete(im.ports, tenantID)
	if inst, ok := im.instances[tenantID]; ok {
		_ = inst.Stop()
		delete(im.instances, tenantID)
	}
}

// GetInstance retrieves the dedicated instance for tenantID, or nil if none exists.
func (im *InstanceManager) GetInstance(tenantID string) *DedicatedInstance {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.instances[tenantID]
}

// GetOrCreateInstance returns the existing instance for tenantID or creates a new one,
// allocating a port if needed.
func (im *InstanceManager) GetOrCreateInstance(tenantID string) (*DedicatedInstance, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	if inst, ok := im.instances[tenantID]; ok {
		return inst, nil
	}

	port, err := im.allocatePortLocked(tenantID)
	if err != nil {
		return nil, err
	}

	inst := &DedicatedInstance{
		TenantID: tenantID,
		Port:     port,
	}
	im.instances[tenantID] = inst
	return inst, nil
}

// GenerateSNIRoutingRules generates Xray routing rules mapping each tenant's domain list
// to an outbound connecting to 127.0.0.1:<tenantPort>, with a fallback rule for other
// traffic connecting to 127.0.0.1:<sharedPort>.
func GenerateSNIRoutingRules(sharedPort int, tenantDomains map[string][]string, tenantPorts map[string]int) map[string]any {
	tenants := make([]string, 0, len(tenantDomains))
	for t := range tenantDomains {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)

	rules := make([]map[string]any, 0)
	outbounds := make([]map[string]any, 0)

	for _, t := range tenants {
		domains := tenantDomains[t]
		if len(domains) == 0 {
			continue
		}
		port, ok := tenantPorts[t]
		if !ok {
			continue
		}

		tag := fmt.Sprintf("outbound-%s", t)
		rules = append(rules, map[string]any{
			"type":        "field",
			"outboundTag": tag,
			"domain":      domains,
		})

		outbounds = append(outbounds, map[string]any{
			"tag":      tag,
			"protocol": "freedom",
			"settings": map[string]any{
				"redirect": fmt.Sprintf("127.0.0.1:%d", port),
			},
		})
	}

	// Fallback rule for sharedPort
	fallbackTag := "outbound-shared"
	rules = append(rules, map[string]any{
		"type":        "field",
		"outboundTag": fallbackTag,
		"network":     "tcp,udp",
	})

	outbounds = append(outbounds, map[string]any{
		"tag":      fallbackTag,
		"protocol": "freedom",
		"settings": map[string]any{
			"redirect": fmt.Sprintf("127.0.0.1:%d", sharedPort),
		},
	})

	return map[string]any{
		"rules":     rules,
		"outbounds": outbounds,
		"routing": map[string]any{
			"rules": rules,
		},
	}
}
