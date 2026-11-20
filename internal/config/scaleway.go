package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxScalewayHistoryMonths caps YAGURA_SCALEWAY_HISTORY_MONTHS, which bounds
// the history's series and its API calls per day.
const MaxScalewayHistoryMonths = 60

// ScalewayConfig holds the Scaleway collector settings.
type ScalewayConfig struct {
	SecretKey      Secret        // YAGURA_SCALEWAY_SECRET_KEY or YAGURA_SCALEWAY_SECRET_KEY_FILE
	OrganizationID string        // YAGURA_SCALEWAY_ORGANIZATION_ID, required when enabled
	Interval       time.Duration // YAGURA_SCALEWAY_INTERVAL
	// HistoryMonths is the number of closed months the monthly history
	// covers (YAGURA_SCALEWAY_HISTORY_MONTHS); 0 turns the history off.
	HistoryMonths   int
	HistoryInterval time.Duration // YAGURA_SCALEWAY_HISTORY_INTERVAL
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
	s.OrganizationID = strings.ToLower(id)
	if s.Interval, err = duration(lookup, "YAGURA_SCALEWAY_INTERVAL", time.Hour); err != nil {
		return nil, err
	}
	s.HistoryMonths = 24
	if v, ok := get(lookup, "YAGURA_SCALEWAY_HISTORY_MONTHS"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > MaxScalewayHistoryMonths {
			// The value is not echoed, like the other settings.
			return nil, fmt.Errorf("YAGURA_SCALEWAY_HISTORY_MONTHS: must be a whole number from 0 to %d", MaxScalewayHistoryMonths)
		}
		s.HistoryMonths = n
	}
	if s.HistoryInterval, err = duration(lookup, "YAGURA_SCALEWAY_HISTORY_INTERVAL", 24*time.Hour); err != nil {
		return nil, err
	}
	// Failed months are retried hourly; a shorter interval would only add calls.
	if s.HistoryInterval < time.Hour {
		return nil, errors.New("YAGURA_SCALEWAY_HISTORY_INTERVAL: must be at least 1h")
	}
	return s, nil
}
