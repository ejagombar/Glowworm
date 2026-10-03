// Package config loads configuration from environment variables (GLOWWORM_*)
// and an optional glowworm.toml file (env wins). Precedence: env > file >
// defaults (doc 13 §3).
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the resolved runtime configuration.
type Config struct {
	Addr         string // GLOWWORM_ADDR
	DataDir      string // GLOWWORM_DATA_DIR
	DBDialect    string // GLOWWORM_DB: sqlite|postgres
	DBDSN        string // GLOWWORM_DB_DSN (postgres)
	Timezone     string // GLOWWORM_TIMEZONE (IANA name)
	SecretKey    string // GLOWWORM_SECRET_KEY (hex or base64, 32 bytes)
	AuthMode     string // GLOWWORM_AUTH_MODE: password|none
	LogFormat    string // GLOWWORM_LOG_FORMAT: text|json
	LogLevel     string // GLOWWORM_LOG_LEVEL
	UploadMaxMB  int    // GLOWWORM_UPLOAD_MAX_MB
	TrustedProxies string // GLOWWORM_TRUSTED_PROXIES (CIDR list, comma separated)
	SetupToken   string // GLOWWORM_SETUP_TOKEN (gates first-run setup page)
	ConfigFile   string // path of the config file that was read ("" = none)
}

// Defaults per doc 13 §3.
func defaults() Config {
	return Config{
		Addr:        "127.0.0.1:8080",
		DataDir:     "./data",
		DBDialect:   "sqlite",
		Timezone:    "Local",
		AuthMode:    "password",
		LogFormat:   "text",
		LogLevel:    "info",
		UploadMaxMB: 25,
	}
}

// Load resolves the configuration: defaults < optional config file < env.
func Load() (Config, error) {
	c := defaults()
	// Optional config file: glowworm.toml in cwd or data dir.
	for _, p := range []string{"glowworm.toml", "data/glowworm.toml"} {
		if fileExists(p) {
			if err := c.loadFile(p); err != nil {
				return c, err
			}
			c.ConfigFile = p
			break
		}
	}
	if v := os.Getenv("GLOWWORM_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("GLOWWORM_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("GLOWWORM_DB"); v != "" {
		c.DBDialect = v
	}
	if v := os.Getenv("GLOWWORM_DB_DSN"); v != "" {
		c.DBDSN = v
	}
	if v := os.Getenv("GLOWWORM_TIMEZONE"); v != "" {
		c.Timezone = v
	}
	if v := os.Getenv("GLOWWORM_SECRET_KEY"); v != "" {
		c.SecretKey = v
	}
	if v := os.Getenv("GLOWWORM_AUTH_MODE"); v != "" {
		c.AuthMode = v
	}
	if v := os.Getenv("GLOWWORM_LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
	if v := os.Getenv("GLOWWORM_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("GLOWWORM_UPLOAD_MAX_MB"); v != "" {
		n, err := fmt.Sscanf(v, "%d", &c.UploadMaxMB)
		if err != nil || n != 1 {
			return c, fmt.Errorf("config: bad GLOWWORM_UPLOAD_MAX_MB %q", v)
		}
	}
	if v := os.Getenv("GLOWWORM_TRUSTED_PROXIES"); v != "" {
		c.TrustedProxies = v
	}
	if v := os.Getenv("GLOWWORM_SETUP_TOKEN"); v != "" {
		c.SetupToken = v
	}
	if c.AuthMode != "password" && c.AuthMode != "none" {
		return c, fmt.Errorf("config: GLOWWORM_AUTH_MODE must be password|none (got %q)", c.AuthMode)
	}
	if c.DBDialect != "sqlite" && c.DBDialect != "postgres" {
		return c, fmt.Errorf("config: GLOWWORM_DB must be sqlite|postgres (got %q)", c.DBDialect)
	}
	return c, nil
}

// DSNSQLite returns the SQLite DSN (the file path inside the data dir).
func (c Config) DSNSQLite() string {
	if filepath.IsAbs(c.DBDSN) && c.DBDSN != "" {
		return c.DBDSN
	}
	return filepath.Join(c.DataDir, "glowworm.db")
}

// loadFile parses a flat glowworm.toml ("key = value" lines; # comments).
// Keys are case-insensitive and mapped to the env names (glowworm_addr, …).
func (c *Config) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	get := func(key string) string {
		env := "GLOWWORM_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(key))
		return os.Getenv(env)
	}
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		// env overrides file — apply only when env unset.
		if get(key) != "" {
			continue
		}
		switch key {
		case "addr":
			c.Addr = val
		case "data_dir":
			c.DataDir = val
		case "db":
			c.DBDialect = val
		case "db_dsn":
			c.DBDSN = val
		case "timezone":
			c.Timezone = val
		case "secret_key":
			c.SecretKey = val
		case "auth_mode":
			c.AuthMode = val
		case "log_format":
			c.LogFormat = val
		case "log_level":
			c.LogLevel = val
		case "upload_max_mb":
			fmt.Sscanf(val, "%d", &c.UploadMaxMB)
		case "trusted_proxies":
			c.TrustedProxies = val
		case "setup_token":
			c.SetupToken = val
		}
	}
	return scan.Err()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
