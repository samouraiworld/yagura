package config

import (
	"errors"
	"strings"
	"time"
)

// ScalewayConfig holds the Scaleway collector settings.
type ScalewayConfig struct {
	SecretKey      Secret        // YAGURA_SCALEWAY_SECRET_KEY or YAGURA_SCALEWAY_SECRET_KEY_FILE
	OrganizationID string        // YAGURA_SCALEWAY_ORGANIZATION_ID, required when enabled
	Interval       time.Duration // YAGURA_SCALEWAY_INTERVAL
}

// loadScaleway reads the YAGURA_SCALEWAY_* variables. It returns nil when no
// secret key is set: the collector is then disabled and the other variables
// are not parsed.
func loadScaleway(lookup LookupFunc) (*ScalewayConfig, error) {
	key, err := secret(lookup, "YAGURA_SCALEWAY_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	if !key.IsSet() {
		return nil, nil
	}
	s := &ScalewayConfig{SecretKey: key}
	id, _ := get(lookup, "YAGURA_SCALEWAY_ORGANIZATION_ID")
	if id = strings.TrimSpace(id); id == "" {
		return nil, errors.New("YAGURA_SCALEWAY_ORGANIZATION_ID: required when YAGURA_SCALEWAY_SECRET_KEY is set")
	}
	// The value is not echoed: a key pasted into the wrong variable must not
	// reach the logs.
	if !uuidPattern.MatchString(id) {
		return nil, errors.New("YAGURA_SCALEWAY_ORGANIZATION_ID: must be an organization ID in UUID form")
	}
	s.OrganizationID = id
	if s.Interval, err = duration(lookup, "YAGURA_SCALEWAY_INTERVAL", time.Hour); err != nil {
		return nil, err
	}
	return s, nil
}
