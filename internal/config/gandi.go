package config

import "time"

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
	g.SharingID, _ = get(lookup, "YAGURA_GANDI_SHARING_ID")
	if g.DomainsInterval, err = duration(lookup, "YAGURA_GANDI_DOMAINS_INTERVAL", 6*time.Hour); err != nil {
		return nil, err
	}
	if g.BillingInterval, err = duration(lookup, "YAGURA_GANDI_BILLING_INTERVAL", time.Hour); err != nil {
		return nil, err
	}
	return g, nil
}
