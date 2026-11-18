package config

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Addr           string
	DataDir        string
	DBDSN          string
	Timezone       string
	SecretKey      string
	AuthMode       string
	LogFormat      string
	LogLevel       string
	UploadMaxMB    int
	TrustedProxies string
	SetupToken     string // "" = one is made on first run; "off" = none
	PublicURL      string // the address people open Glowworm at, e.g. https://money.example.com
	OwnerUsername  string
	OwnerPassword  string
	ExchangeRates  string // "ecb" (default) fetches daily rates; "off" = typed-in rates only
	ConfigFile     string
}

func defaults() Config {
	return Config{
		Addr:        "127.0.0.1:8080",
		DataDir:     "./data",
		Timezone:    "Local",
		AuthMode:    "password",
		LogFormat:   "text",
		LogLevel:    "info",
		UploadMaxMB: 25,
	}
}

func Load() (Config, error) {
	c := defaults()
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
	if v := os.Getenv("GLOWWORM_EXCHANGE_RATES"); v != "" {
		c.ExchangeRates = v
	}
	if v := os.Getenv("GLOWWORM_OWNER_USERNAME"); v != "" {
		c.OwnerUsername = v
	}
	if v := os.Getenv("GLOWWORM_OWNER_PASSWORD"); v != "" {
		c.OwnerPassword = v
	}
	if v := os.Getenv("GLOWWORM_SETUP_TOKEN"); v != "" {
		c.SetupToken = v
	}
	if v := os.Getenv("GLOWWORM_PUBLIC_URL"); v != "" {
		c.PublicURL = v
	}
	if c.PublicURL != "" {
		u, err := cleanPublicURL(c.PublicURL)
		if err != nil {
			return c, err
		}
		c.PublicURL = u
	}
	if c.AuthMode != "password" && c.AuthMode != "none" {
		return c, fmt.Errorf("config: GLOWWORM_AUTH_MODE must be password|none (got %q)", c.AuthMode)
	}
	return c, nil
}

func (c Config) DSNSQLite() string {
	if filepath.IsAbs(c.DBDSN) && c.DBDSN != "" {
		return c.DBDSN
	}
	return filepath.Join(c.DataDir, "glowworm.db")
}

func (c *Config) loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
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
		if get(key) != "" {
			continue
		}
		switch key {
		case "addr":
			c.Addr = val
		case "data_dir":
			c.DataDir = val
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
			if _, err := fmt.Sscanf(val, "%d", &c.UploadMaxMB); err != nil || c.UploadMaxMB <= 0 {
				return fmt.Errorf("config: upload_max_mb must be a whole number of megabytes, not %q", val)
			}
		case "trusted_proxies":
			c.TrustedProxies = val
		case "exchange_rates":
			c.ExchangeRates = val
		case "owner_username":
			c.OwnerUsername = val
		case "owner_password":
			c.OwnerPassword = val
		case "setup_token":
			c.SetupToken = val
		case "public_url":
			c.PublicURL = val
		}
	}
	return scan.Err()
}

// cleanPublicURL checks the public address is a whole http(s) address at the
// root of a host, and drops a trailing slash.
func cleanPublicURL(raw string) (string, error) {
	bad := fmt.Errorf("config: GLOWWORM_PUBLIC_URL must be like https://money.example.com (got %q)", raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return "", bad
	}
	if p := u.EscapedPath(); p != "" && p != "/" {
		return "", fmt.Errorf("config: GLOWWORM_PUBLIC_URL can't have a path: Glowworm has to be at the root of its address (got %q)", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
