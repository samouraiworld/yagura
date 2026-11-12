package config

import "time"

// NetlifyConfig holds the Netlify collector settings.
type NetlifyConfig struct {
	Token           Secret        // YAGURA_NETLIFY_TOKEN or YAGURA_NETLIFY_TOKEN_FILE
	DeploysInterval time.Duration // YAGURA_NETLIFY_DEPLOYS_INTERVAL (sites and last deploy)
	BuildsInterval  time.Duration // YAGURA_NETLIFY_BUILDS_INTERVAL (build minutes)
}

// loadNetlify reads the YAGURA_NETLIFY_* variables. It returns nil when no
// token is set: the collector is then disabled and the other variables are
// not parsed.
func loadNetlify(lookup LookupFunc) (*NetlifyConfig, error) {
	token, err := secret(lookup, "YAGURA_NETLIFY_TOKEN")
	if err != nil {
		return nil, err
	}
	if !token.IsSet() {
		return nil, nil
	}
	n := &NetlifyConfig{Token: token}
	if n.DeploysInterval, err = duration(lookup, "YAGURA_NETLIFY_DEPLOYS_INTERVAL", 5*time.Minute); err != nil {
		return nil, err
	}
	if n.BuildsInterval, err = duration(lookup, "YAGURA_NETLIFY_BUILDS_INTERVAL", 15*time.Minute); err != nil {
		return nil, err
	}
	return n, nil
}
