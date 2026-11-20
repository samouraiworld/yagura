package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tAPIToken is a fake token, built at run time.
var tAPIToken = "fake-api-" + strings.Repeat("0123456789", 4)

func TestLoadBudgetOff(t *testing.T) {
	c, err := Load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Budget != nil || c.API != nil {
		t.Errorf("budget or API on by default: %+v %+v", c.Budget, c.API)
	}
}

func TestLoadBudgetStoreOnly(t *testing.T) {
	c, err := Load(mapLookup(map[string]string{
		"YAGURA_STATE_DIR":               " /var/lib/yagura ",
		"YAGURA_BUDGET_INITIAL_TOTAL":    "250",
		"YAGURA_BUDGET_INITIAL_SCALEWAY": "120,5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := BudgetConfig{StateDir: "/var/lib/yagura", InitialTotal: 250, InitialScaleway: 120.5}
	if c.Budget == nil || *c.Budget != want || c.API != nil {
		t.Errorf("Budget = %+v, API = %+v", c.Budget, c.API)
	}
}

func TestLoadAPI(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(tAPIToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]map[string]string{
		"inline": {"YAGURA_API_TOKEN": tAPIToken},
		"file":   {"YAGURA_API_TOKEN_FILE": file},
	} {
		t.Run(name, func(t *testing.T) {
			env["YAGURA_STATE_DIR"] = "/state"
			env["YAGURA_API_LISTEN_ADDRESS"] = "127.0.0.1:10101"
			c, err := Load(mapLookup(env))
			if err != nil {
				t.Fatal(err)
			}
			if c.API == nil || c.API.ListenAddress != "127.0.0.1:10101" || c.Budget == nil {
				t.Fatalf("API = %+v", c.API)
			}
			if v, err := c.API.Token.Read(); err != nil || v != tAPIToken {
				t.Errorf("Token.Read() = %q, %v", v, err)
			}
			if s := fmt.Sprintf("%v %+v %#v", c.API, c.API, c.API); strings.Contains(s, tAPIToken) {
				t.Errorf("the token leaks through fmt: %s", s)
			}
		})
	}
}

func TestLoadBudgetErrors(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"blank state dir":     {map[string]string{"YAGURA_STATE_DIR": "  "}, "YAGURA_STATE_DIR"},
		"seed without dir":    {map[string]string{"YAGURA_BUDGET_INITIAL_TOTAL": "250"}, "YAGURA_BUDGET_INITIAL_TOTAL: requires YAGURA_STATE_DIR"},
		"bad seed":            {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_BUDGET_INITIAL_SCALEWAY": "-1"}, "YAGURA_BUDGET_INITIAL_SCALEWAY"},
		"zero seed":           {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_BUDGET_INITIAL_TOTAL": "0"}, "YAGURA_BUDGET_INITIAL_TOTAL"},
		"token without API":   {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_TOKEN": tAPIToken}, "YAGURA_API_LISTEN_ADDRESS"},
		"API without token":   {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_LISTEN_ADDRESS": ":10101"}, "YAGURA_API_TOKEN"},
		"API without dir":     {map[string]string{"YAGURA_API_LISTEN_ADDRESS": ":10101", "YAGURA_API_TOKEN": tAPIToken}, "YAGURA_STATE_DIR"},
		"blank API address":   {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_LISTEN_ADDRESS": " ", "YAGURA_API_TOKEN": tAPIToken}, "YAGURA_API_LISTEN_ADDRESS"},
		"both token forms":    {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_LISTEN_ADDRESS": ":1", "YAGURA_API_TOKEN": tAPIToken, "YAGURA_API_TOKEN_FILE": "/t"}, "set only one"},
		"same listen address": {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_LISTEN_ADDRESS": ":10100", "YAGURA_API_TOKEN": tAPIToken}, "must differ"},
		"blank inline token":  {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_API_LISTEN_ADDRESS": ":1", "YAGURA_API_TOKEN": "  "}, "YAGURA_API_TOKEN"},
		"token in seed var":   {map[string]string{"YAGURA_STATE_DIR": "/s", "YAGURA_BUDGET_INITIAL_TOTAL": tAPIToken}, "YAGURA_BUDGET_INITIAL_TOTAL"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(mapLookup(c.env))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if strings.Contains(err.Error(), tAPIToken) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}
