package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunConfigError(t *testing.T) {
	env := map[string]string{"YAGURA_LOG_LEVEL": "loud"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	err := run(context.Background(), lookup, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "YAGURA_LOG_LEVEL") {
		t.Fatalf("got %v", err)
	}
}
