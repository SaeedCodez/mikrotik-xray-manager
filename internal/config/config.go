// Package config loads application configuration from environment variables.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all runtime configuration.
type Config struct {
	Password         string // APP_PASSWORD (required)
	Port             int    // APP_PORT
	DataDir          string // DATA_DIR
	XrayBinary       string // XRAY_BINARY
	XrayConfigPath   string // XRAY_CONFIG_PATH
	InboundSocks     int    // XRAY_INBOUND_PORT
	InboundHTTP      int    // XRAY_INBOUND_HTTP_PORT
	HealthTestURL    string // HEALTH_TEST_URL — probed through the proxy for real delay
	SessionSecret    []byte // SESSION_SECRET
	sessionEphemeral bool
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() *Config {
	c := &Config{
		Password:       os.Getenv("APP_PASSWORD"),
		Port:           envInt("APP_PORT", 8080),
		DataDir:        envStr("DATA_DIR", "./data"),
		XrayBinary:     envStr("XRAY_BINARY", "/usr/local/bin/xray"),
		XrayConfigPath: os.Getenv("XRAY_CONFIG_PATH"),
		InboundSocks:   envInt("XRAY_INBOUND_PORT", 10808),
		InboundHTTP:    envInt("XRAY_INBOUND_HTTP_PORT", 10809),
		HealthTestURL:  envStr("HEALTH_TEST_URL", "http://www.gstatic.com/generate_204"),
	}

	if c.XrayConfigPath == "" {
		c.XrayConfigPath = filepath.Join(c.DataDir, "xray", "config.json")
	}

	if secret := os.Getenv("SESSION_SECRET"); secret != "" {
		c.SessionSecret = []byte(secret)
	} else {
		// Generate an ephemeral secret so the app still runs; sessions won't
		// survive a restart, which is acceptable for a single-user tool.
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Fatalf("config: failed to generate session secret: %v", err)
		}
		c.SessionSecret = []byte(hex.EncodeToString(buf))
		c.sessionEphemeral = true
	}

	if c.Password == "" {
		// Don't crash — log a loud warning. A blank password means any input
		// unlocks (matching the prototype's "demo" behaviour), but we warn so
		// it's never an accident in production.
		log.Println("WARNING: APP_PASSWORD is not set — the app will accept any password.")
	}
	if c.sessionEphemeral {
		log.Println("WARNING: SESSION_SECRET not set — using an ephemeral secret; sessions reset on restart.")
	}

	return c
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("config: %s=%q is not a valid integer, using default %d", key, v, def)
	}
	return def
}
