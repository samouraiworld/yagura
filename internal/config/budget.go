package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samouraiworld/yagura/internal/budget"
)

// BudgetConfig holds the budget store settings.
type BudgetConfig struct {
	StateDir        string  // YAGURA_STATE_DIR
	InitialTotal    float64 // YAGURA_BUDGET_INITIAL_TOTAL, 0 when unset
	InitialScaleway float64 // YAGURA_BUDGET_INITIAL_SCALEWAY, 0 when unset
}

// APIConfig holds the budget write API settings.
type APIConfig struct {
	ListenAddress string // YAGURA_API_LISTEN_ADDRESS
	Token         Secret // YAGURA_API_TOKEN or YAGURA_API_TOKEN_FILE
}

// loadBudget reads the budget store and write API variables. The store is
// on when YAGURA_STATE_DIR is set; the API is on when
// YAGURA_API_LISTEN_ADDRESS is set, and then needs the store and a token.
func loadBudget(lookup LookupFunc) (*BudgetConfig, *APIConfig, error) {
	var b *BudgetConfig
	if dir, ok := get(lookup, "YAGURA_STATE_DIR"); ok {
		if dir = strings.TrimSpace(dir); dir == "" {
			return nil, nil, errors.New("YAGURA_STATE_DIR: path is empty")
		}
		b = &BudgetConfig{StateDir: dir}
	}
	for _, seed := range []struct {
		name string
		dst  func(*BudgetConfig) *float64
	}{
		{"YAGURA_BUDGET_INITIAL_TOTAL", func(c *BudgetConfig) *float64 { return &c.InitialTotal }},
		{"YAGURA_BUDGET_INITIAL_SCALEWAY", func(c *BudgetConfig) *float64 { return &c.InitialScaleway }},
	} {
		v, ok := get(lookup, seed.name)
		if !ok {
			continue
		}
		if b == nil {
			return nil, nil, fmt.Errorf("%s: requires YAGURA_STATE_DIR", seed.name)
		}
		amount, err := budget.ParseAmount(v)
		if err != nil {
			// The value is not echoed, like the other settings.
			return nil, nil, fmt.Errorf("%s: %w", seed.name, err)
		}
		*seed.dst(b) = amount
	}

	token, err := secret(lookup, "YAGURA_API_TOKEN")
	if err != nil {
		return nil, nil, err
	}
	addr, ok := get(lookup, "YAGURA_API_LISTEN_ADDRESS")
	if !ok {
		if token.IsSet() {
			return nil, nil, errors.New("YAGURA_API_TOKEN: set while YAGURA_API_LISTEN_ADDRESS is not; set both to turn the API on")
		}
		return b, nil, nil
	}
	if addr = strings.TrimSpace(addr); addr == "" {
		return nil, nil, errors.New("YAGURA_API_LISTEN_ADDRESS: address is empty")
	}
	if !token.IsSet() {
		return nil, nil, errors.New("YAGURA_API_TOKEN or YAGURA_API_TOKEN_FILE: required when YAGURA_API_LISTEN_ADDRESS is set")
	}
	if b == nil {
		return nil, nil, errors.New("YAGURA_STATE_DIR: required when YAGURA_API_LISTEN_ADDRESS is set")
	}
	return b, &APIConfig{ListenAddress: addr, Token: token}, nil
}
