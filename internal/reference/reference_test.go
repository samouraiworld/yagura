package reference

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestReadmeMetricReference(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `(yagura_[a-z0-9_]+)`")
	var got []string
	for _, line := range strings.Split(string(data), "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			got = append(got, m[1])
		}
	}
	slices.Sort(got)
	got = slices.Compact(got)
	want := MetricNames()
	for _, n := range want {
		if !slices.Contains(got, n) {
			t.Errorf("metric %s is missing from the README reference", n)
		}
	}
	for _, n := range got {
		if !slices.Contains(want, n) {
			t.Errorf("README documents %s, which yagura does not export", n)
		}
	}
}

func TestDashboardsLint(t *testing.T) {
	files, err := filepath.Glob("../../dashboards/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no dashboard found")
	}
	token := regexp.MustCompile(`yagura_[a-z0-9_]+`)
	names := MetricNames()
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // path comes from a fixed glob
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: invalid JSON: %v", f, err)
			continue
		}
		if doc["uid"] != "yagura-overview" {
			t.Errorf("%s: uid = %v, want yagura-overview", f, doc["uid"])
		}
		if !hasDatasourceVariable(doc) {
			t.Errorf("%s: missing datasource template variable", f)
		}
		walk(doc, func(key string, v any) {
			switch key {
			case "datasource":
				switch d := v.(type) {
				case string:
					if d != "${datasource}" {
						t.Errorf("%s: datasource %q, want ${datasource}", f, d)
					}
				case map[string]any:
					if d["uid"] != "${datasource}" {
						t.Errorf("%s: datasource uid %v, want ${datasource}", f, d["uid"])
					}
				default:
					t.Errorf("%s: unexpected datasource value %v", f, v)
				}
			case "expr":
				s, _ := v.(string)
				for _, m := range token.FindAllString(s, -1) {
					if !known(names, m) {
						t.Errorf("%s: expr uses unknown metric %s", f, m)
					}
				}
			}
		})
	}
}

func known(names []string, m string) bool {
	if slices.Contains(names, m) {
		return true
	}
	for _, suf := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(m, suf); ok && slices.Contains(names, base) {
			return true
		}
	}
	return false
}

func hasDatasourceVariable(doc map[string]any) bool {
	tpl, _ := doc["templating"].(map[string]any)
	list, _ := tpl["list"].([]any)
	for _, e := range list {
		v, _ := e.(map[string]any)
		if v["name"] == "datasource" && v["type"] == "datasource" && v["query"] == "prometheus" {
			return true
		}
	}
	return false
}

func walk(v any, fn func(key string, v any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			fn(k, c)
			walk(c, fn)
		}
	case []any:
		for _, c := range t {
			walk(c, fn)
		}
	}
}
