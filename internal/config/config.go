// Package config loads yagura settings from YAGURA_* environment variables.
package config

import (
	"errors"
	"log/slog"
)

// Config holds the settings shared by all providers.
type Config struct {
	ListenAddress string     // YAGURA_LISTEN_ADDRESS
	WebConfigFile string     // YAGURA_WEB_CONFIG_FILE
	LogLevel      slog.Level // YAGURA_LOG_LEVEL
	LogFormat     string     // YAGURA_LOG_FORMAT
	Statuspage    StatuspageConfig
	Gandi         *GandiConfig    // nil when YAGURA_GANDI_TOKEN is unset
	Scaleway      *ScalewayConfig // nil when YAGURA_SCALEWAY_SECRET_KEY is unset
}

// Load builds the configuration from lookup, validating every value.
func Load(lookup LookupFunc) (*Config, error) {
	c := &Config{
		ListenAddress: ":10100",
		LogLevel:      slog.LevelInfo,
		LogFormat:     "logfmt",
	}
	if v, ok := get(lookup, "YAGURA_LISTEN_ADDRESS"); ok {
		c.ListenAddress = v
	}
	c.WebConfigFile, _ = get(lookup, "YAGURA_WEB_CONFIG_FILE")
	if v, ok := get(lookup, "YAGURA_LOG_LEVEL"); ok {
		switch v {
		case "debug":
			c.LogLevel = slog.LevelDebug
		case "info":
			c.LogLevel = slog.LevelInfo
		case "warn":
			c.LogLevel = slog.LevelWarn
		case "error":
			c.LogLevel = slog.LevelError
		default:
			return nil, errors.New("YAGURA_LOG_LEVEL: must be one of debug, info, warn, error")
		}
	}
	if v, ok := get(lookup, "YAGURA_LOG_FORMAT"); ok {
		if v != "logfmt" && v != "json" {
			return nil, errors.New("YAGURA_LOG_FORMAT: must be one of logfmt, json")
		}
		c.LogFormat = v
	}
	var err error
	if c.Statuspage, err = loadStatuspage(lookup); err != nil {
		return nil, err
	}
	if c.Gandi, err = loadGandi(lookup); err != nil {
		return nil, err
	}
	if c.Scaleway, err = loadScaleway(lookup); err != nil {
		return nil, err
	}
	return c, nil
}
