package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const minInterval = 30 * time.Second

// LookupFunc resolves an environment variable; os.LookupEnv in main.
type LookupFunc func(key string) (string, bool)

// get returns the value of name, treating an empty value as unset.
func get(lookup LookupFunc, name string) (string, bool) {
	v, ok := lookup(name)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// secret reads the secret X or X_FILE; setting both is an error.
func secret(lookup LookupFunc, name string) (Secret, error) {
	inline, hasInline := get(lookup, name)
	file, hasFile := get(lookup, name+"_FILE")
	switch {
	case hasInline && hasFile:
		return Secret{}, fmt.Errorf("%s and %s_FILE are both set; set only one", name, name)
	case hasFile:
		return Secret{name: name + "_FILE", file: file}, nil
	case hasInline:
		return Secret{name: name, inline: inline}, nil
	}
	return Secret{name: name}, nil
}

// duration parses a Go duration, enforcing the 30s minimum.
func duration(lookup LookupFunc, name string, def time.Duration) (time.Duration, error) {
	v, ok := get(lookup, name)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration: %w", name, err)
	}
	if d < minInterval {
		return 0, fmt.Errorf("%s: must be at least 30s", name)
	}
	return d, nil
}

// list splits a comma-separated variable, trimming items and dropping empties.
func list(lookup LookupFunc, name, def string) []string {
	v, ok := get(lookup, name)
	if !ok {
		v = def
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// boolean parses a boolean as strconv.ParseBool does.
func boolean(lookup LookupFunc, name string, def bool) (bool, error) {
	v, ok := get(lookup, name)
	if !ok {
		return def, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", name, v)
	}
	return b, nil
}
