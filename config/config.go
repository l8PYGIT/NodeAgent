package config

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

type Config struct {
	ServicePort                 int
	NodeHost                    string
	XrayExecutablePath          string
	XrayAssetsPath              string
	SslCertFile                 string
	SslKeyFile                  string
	ApiKey                      uuid.UUID
	ServiceProtocol             string
	Debug                       bool
	GeneratedConfigPath         string
	LogBufferSize               int
	StartupLogTailSize          int
	StatsUpdateIntervalSeconds  int
	StatsCleanupIntervalSeconds int
}

func Load() (*Config, error) {
	err := godotenv.Load()
	if err != nil {
		log.Printf("[Warning] Failed to load env file, if you're using 'Docker' and you set 'environment' or 'env_file' variable, don't worry, everything is fine. Error: %v", err)
	}

	cfg := &Config{
		ServicePort:                 GetEnvAsInt("SERVICE_PORT", 62050),
		XrayExecutablePath:          GetEnv("XRAY_EXECUTABLE_PATH", "/usr/local/bin/xray"),
		XrayAssetsPath:              GetEnv("XRAY_ASSETS_PATH", "/usr/local/share/xray"),
		SslCertFile:                 GetEnv("SSL_CERT_FILE", "/var/lib/pg-node/certs/ssl_cert.pem"),
		SslKeyFile:                  GetEnv("SSL_KEY_FILE", "/var/lib/pg-node/certs/ssl_key.pem"),
		GeneratedConfigPath:         GetEnv("GENERATED_CONFIG_PATH", "/var/lib/pg-node/generated/"),
		ServiceProtocol:             GetEnv("SERVICE_PROTOCOL", "grpc"),
		Debug:                       GetEnvAsBool("DEBUG", false),
		LogBufferSize:               GetEnvAsInt("LOG_BUFFER_SIZE", 10000),
		StartupLogTailSize:          GetEnvAsInt("STARTUP_LOG_TAIL_SIZE", 200),
		StatsUpdateIntervalSeconds:  GetEnvAsInt("STATS_UPDATE_INTERVAL_SECONDS", 10),
		StatsCleanupIntervalSeconds: GetEnvAsInt("STATS_CLEANUP_INTERVAL_SECONDS", 300),
	}

	if cfg.LogBufferSize <= 0 {
		log.Printf("[Warning] LOG_BUFFER_SIZE must be greater than 0, got %d. Falling back to 1.", cfg.LogBufferSize)
		cfg.LogBufferSize = 1
	}

	// API_KEY must be a real UUID. GetEnvAsUUID returns uuid.Nil on failure, and
	// the REST/gRPC middlewares compare the presented key against whatever is
	// here — so merely logging the error left a node that accepts the nil UUID
	// (00000000-0000-0000-0000-000000000000) from anyone who can reach the port,
	// granting full control of the core. Fail to load instead.
	cfg.ApiKey, err = GetEnvAsUUID("API_KEY")
	if err != nil {
		return nil, fmt.Errorf("API_KEY must be a valid UUID: %w", err)
	}
	if cfg.ApiKey == uuid.Nil {
		return nil, fmt.Errorf("API_KEY must not be the nil UUID")
	}

	nodeHostStr := GetEnv("NODE_HOST", "0.0.0.0")
	ipPattern := `^(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$`
	re := regexp.MustCompile(ipPattern)

	if re.MatchString(nodeHostStr) {
		cfg.NodeHost = nodeHostStr
	} else {
		log.Println(nodeHostStr, " is not a valid IP address.\n NODE_HOST will be set to 127.0.0.1")
		cfg.NodeHost = "127.0.0.1"
	}

	return cfg, nil
}

// NewTestConfig creates a config for testing. It does NOT go through Load,
// which validates API_KEY and fails without one — tests supply the key directly.
func NewTestConfig(generatedConfigPath string, key uuid.UUID) *Config {
	return &Config{
		ServicePort:                 GetEnvAsInt("SERVICE_PORT", 62050),
		NodeHost:                    "127.0.0.1",
		XrayExecutablePath:          GetEnv("XRAY_EXECUTABLE_PATH", "/usr/local/bin/xray"),
		XrayAssetsPath:              GetEnv("XRAY_ASSETS_PATH", "/usr/local/share/xray"),
		SslCertFile:                 GetEnv("SSL_CERT_FILE", "/var/lib/pg-node/certs/ssl_cert.pem"),
		SslKeyFile:                  GetEnv("SSL_KEY_FILE", "/var/lib/pg-node/certs/ssl_key.pem"),
		GeneratedConfigPath:         generatedConfigPath,
		ServiceProtocol:             GetEnv("SERVICE_PROTOCOL", "grpc"),
		Debug:                       GetEnvAsBool("DEBUG", false),
		LogBufferSize:               GetEnvAsInt("LOG_BUFFER_SIZE", 10000),
		StartupLogTailSize:          GetEnvAsInt("STARTUP_LOG_TAIL_SIZE", 200),
		StatsUpdateIntervalSeconds:  GetEnvAsInt("STATS_UPDATE_INTERVAL_SECONDS", 10),
		StatsCleanupIntervalSeconds: GetEnvAsInt("STATS_CLEANUP_INTERVAL_SECONDS", 300),
		ApiKey:                      key,
	}
}

func GetEnv(key, fallback string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		value = fallback
	}
	return value
}

func GetEnvAsBool(name string, defaultVal bool) bool {
	valStr := GetEnv(name, "")
	if val, err := strconv.ParseBool(valStr); err == nil {
		return val
	}
	return defaultVal
}

func GetEnvAsInt(name string, defaultVal int) int {
	valStr := GetEnv(name, "")
	if val, err := strconv.Atoi(valStr); err == nil {
		return val
	}
	return defaultVal
}

func GetEnvAsUUID(name string) (uuid.UUID, error) {
	valStr := GetEnv(name, "")

	val, err := uuid.Parse(valStr)
	if err != nil {
		return uuid.Nil, err
	}
	return val, nil
}
