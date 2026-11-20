// Package budget stores monthly budgets per scope and business project,
// maps Scaleway projects to business projects, and exposes both as metrics.
package budget

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Scopes of a budget.
const (
	ScopeTotal    = "total"
	ScopeScaleway = "scaleway"
	ScopeProject  = "project"
)

// Currency is the only currency budgets are kept in.
const Currency = "EUR"

// MaxAmount caps a budget, in Currency.
const MaxAmount = 100000

var (
	projectPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	projectIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// amountPattern accepts a plain decimal with a dot or a comma and at
	// most two decimals: no sign, exponent, grouping or hexadecimal form.
	amountPattern = regexp.MustCompile(`^[0-9]{1,6}([.,][0-9]{1,2})?$`)
)

// ErrAmount is returned for an amount that is not a valid budget.
var ErrAmount = errors.New("amount must be a number greater than 0 and at most 100000, with at most two decimals (300, 300.5 or 300,5)")

// ParseAmount parses a budget amount typed by a person: surrounding spaces
// are ignored and the decimal separator is a dot or a comma.
func ParseAmount(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if !amountPattern.MatchString(s) {
		return 0, ErrAmount
	}
	v, err := strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
	if err != nil || !validAmount(v) {
		return 0, ErrAmount
	}
	return v, nil
}

func validAmount(v float64) bool { return v > 0 && v <= MaxAmount }

// ValidProject reports whether s is a valid business project name.
func ValidProject(s string) bool { return projectPattern.MatchString(s) }

// ValidScope reports whether s is a budget scope.
func ValidScope(s string) bool {
	return s == ScopeTotal || s == ScopeScaleway || s == ScopeProject
}

// NormalizeProjectID trims and lowercases a Scaleway project ID and reports
// whether the result is a UUID.
func NormalizeProjectID(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, projectIDPattern.MatchString(s)
}

// AutoProject returns the business project of a Scaleway project by the
// automatic rule: the name is cut at the first "-", trimmed, lowercased, and
// its spaces become "-". An empty name, or a result that is not a valid
// project name (accents, "_", tabs...), falls back to the project ID.
func AutoProject(name, projectID string) string {
	if i := strings.IndexByte(name, '-'); i >= 0 {
		name = name[:i]
	}
	// Trimming after the cut maps "Acme - prod" to "acme", like "Acme-prod".
	name = strings.TrimSpace(name)
	p := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	if !ValidProject(p) {
		return projectID
	}
	return p
}
