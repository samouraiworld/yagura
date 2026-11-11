package config

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// uuidPattern matches the 8-4-4-4-12 hexadecimal form of an organization ID
// (Gandi sharing ID, Scaleway organization ID). Pollers put the ID in a URL
// path or query, so nothing else may pass.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// GandiConfig holds the Gandi collector settings.
type GandiConfig struct {
	Token           Secret        // YAGURA_GANDI_TOKEN or YAGURA_GANDI_TOKEN_FILE
	SharingID       string        // YAGURA_GANDI_SHARING_ID, empty for the token's default scope
	DomainsInterval time.Duration // YAGURA_GANDI_DOMAINS_INTERVAL
	BillingInterval time.Duration // YAGURA_GANDI_BILLING_INTERVAL
}

// loadGandi reads the YAGURA_GANDI_* variables. It returns nil when no token
// is set: the collector is then disabled and the other variables are not
// parsed.
func loadGandi(lookup LookupFunc) (*GandiConfig, error) {
	token, err := secret(lookup, "YAGURA_GANDI_TOKEN")
	if err != nil {
		return nil, err
	}
	if !token.IsSet() {
		return nil, nil
	}
	g := &GandiConfig{Token: token}
	if id, ok := get(lookup, "YAGURA_GANDI_SHARING_ID"); ok {
		// The value is not echoed: a token pasted into the wrong variable
		// must not reach the logs.
		if id = strings.TrimSpace(id); !uuidPattern.MatchString(id) {
			return nil, errors.New("YAGURA_GANDI_SHARING_ID: must be an organization ID in UUID form")
		}
		g.SharingID = id
	}
	if g.DomainsInterval, err = duration(lookup, "YAGURA_GANDI_DOMAINS_INTERVAL", 6*time.Hour); err != nil {
		return nil, err
	}
	if g.BillingInterval, err = duration(lookup, "YAGURA_GANDI_BILLING_INTERVAL", time.Hour); err != nil {
		return nil, err
	}
	return g, nil
}
