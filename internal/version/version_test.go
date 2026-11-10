package version_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samourai-crew/yagura/internal/version"
)

func TestBuildInfo(t *testing.T) {
	expected := `# HELP yagura_build_info Build information about the yagura exporter; the value is always 1.
# TYPE yagura_build_info gauge
yagura_build_info{goversion="` + runtime.Version() + `",revision="unknown",version="dev"} 1
`
	if err := testutil.CollectAndCompare(version.NewCollector(), strings.NewReader(expected)); err != nil {
		t.Fatal(err)
	}
}

func TestUserAgent(t *testing.T) {
	const want = "yagura/dev (+https://github.com/samourai-crew/yagura)"
	if got := version.UserAgent(); !strings.HasPrefix(got, want) {
		t.Fatalf("UserAgent() = %q, want prefix %q", got, want)
	}
}
