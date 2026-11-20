// Package api serves the budget write API: list and change the budgets and
// the manual mapping of Scaleway projects to business projects. Every /api/
// route needs a bearer token. Changes touch only yagura's own local store,
// never a provider.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/samouraiworld/yagura/internal/budget"
	"github.com/samouraiworld/yagura/internal/scaleway"
)

// MinTokenLength is the shortest token CheckToken accepts.
const MinTokenLength = 32

// Options configures Handler.
type Options struct {
	Store *budget.Store
	// Projects lists the Scaleway projects seen in billing; nil when the
	// Scaleway collector is off.
	Projects func() []scaleway.Project
	// Token returns the expected bearer token; it is called on every
	// request so that a rotated token file is picked up.
	Token  func() (string, error)
	Logger *slog.Logger
}

// CheckToken reads the token once, so that a missing or short token is a
// startup error. Errors never include the value.
func CheckToken(token func() (string, error)) error {
	v, err := token()
	if err != nil {
		return err
	}
	if len(v) < MinTokenLength {
		return fmt.Errorf("API token: must be at least %d characters", MinTokenLength)
	}
	return nil
}

type handler struct {
	Options
}

// Handler returns the API handler. Paths outside /api/ get 404; every /api/
// path, known or not, needs the bearer token first.
func Handler(opts Options) http.Handler {
	h := &handler{Options: opts}
	mux := http.NewServeMux()
	route := func(method, path string, f http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, f)
		// Other methods on a known path get a JSON 405 rather than the
		// mux's plain-text one. GET routes also serve HEAD.
		allow := method
		if method == http.MethodGet {
			allow = "GET, HEAD"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed; use "+method)
		})
	}
	route(http.MethodGet, "/api/v1/budgets", h.listBudgets)
	route(http.MethodPost, "/api/v1/budgets/set", h.setBudget)
	route(http.MethodPost, "/api/v1/budgets/unset", h.unsetBudget)
	route(http.MethodGet, "/api/v1/projects", h.listProjects)
	route(http.MethodPost, "/api/v1/projects/set", h.setProject)
	route(http.MethodPost, "/api/v1/projects/unset", h.unsetProject)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if !h.authorized(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// maxAuthHeader caps the Authorization header the API hashes.
const maxAuthHeader = 1 << 10

// authorized checks the bearer token in constant time. The token and the
// Authorization header are never logged. A request without a usable header
// gets 401 before the token is read.
func (h *handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	got, ok := bearer(header)
	if !ok || len(header) > maxAuthHeader {
		h.unauthorized(w, r)
		return false
	}
	want, err := h.Token()
	if err == nil && len(want) < MinTokenLength {
		// A rotated token file must not weaken the startup check.
		err = fmt.Errorf("API token: shorter than %d characters; every request is refused", MinTokenLength)
	}
	if err != nil {
		h.Logger.Error("cannot use the API token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	gotSum, wantSum := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
		h.unauthorized(w, r)
		return false
	}
	return true
}

func (h *handler) unauthorized(w http.ResponseWriter, r *http.Request) {
	h.Logger.Warn("API request rejected: missing or wrong token",
		"method", capped(r.Method), "path", capped(r.URL.Path), "remote_addr", r.RemoteAddr)
	w.Header().Set("WWW-Authenticate", `Bearer realm="yagura"`)
	writeError(w, http.StatusUnauthorized, "unauthorized")
}

// bearer extracts the token of an "Authorization: Bearer <token>" header;
// the scheme is case-insensitive.
func bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// maxLogValue caps request strings written to the logs.
const maxLogValue = 64

func capped(s string) string {
	if len(s) <= maxLogValue {
		return s
	}
	return s[:maxLogValue] + "...(truncated)"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// storeError maps a store error to a response; a save failure is logged and
// reported without detail.
func (h *handler) storeError(w http.ResponseWriter, err error) {
	var inv *budget.InvalidError
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.Error())
	case errors.Is(err, budget.ErrLimit):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		h.Logger.Error("cannot save the budget store", "error", err)
		writeError(w, http.StatusInternalServerError, "cannot save the change")
	}
}

func (h *handler) projects() []scaleway.Project {
	if h.Projects == nil {
		return nil
	}
	return h.Projects()
}

func (h *handler) resolved() []budget.Resolved {
	return budget.Resolve(h.projects(), h.Store.Mappings())
}

func formatAmount(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// logValue renders an optional value for the change log.
func logValue[T any](v *T) any {
	if v == nil {
		return "none"
	}
	return *v
}

func (h *handler) listBudgets(w http.ResponseWriter, _ *http.Request) {
	budgets := h.Store.Budgets()
	if budgets == nil {
		budgets = []budget.Budget{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": budgets})
}

type budgetChange struct {
	Action    string   `json:"action"`
	Scope     string   `json:"scope"`
	Project   string   `json:"project"`
	Currency  string   `json:"currency"`
	OldAmount *float64 `json:"old_amount"`
	NewAmount *float64 `json:"new_amount"`
	Message   string   `json:"message"`
	Warnings  []string `json:"warnings"`
}

func budgetLabel(scope, project string) string {
	if scope == budget.ScopeProject {
		return "project " + project
	}
	return scope
}

func (h *handler) logBudget(r *http.Request, c budgetChange) {
	h.Logger.Info("budget changed", "action", c.Action, "scope", c.Scope, "project", c.Project,
		"old_amount", logValue(c.OldAmount), "new_amount", logValue(c.NewAmount),
		"currency", c.Currency, "remote_addr", r.RemoteAddr)
}

func (h *handler) setBudget(w http.ResponseWriter, r *http.Request) {
	p, ok := readParams(w, r)
	if !ok {
		return
	}
	// Budgets are kept in EUR only; another currency is refused rather
	// than stored as EUR.
	if c, has := p["currency"]; has && !strings.EqualFold(c, budget.Currency) {
		writeError(w, http.StatusBadRequest, "currency must be "+budget.Currency)
		return
	}
	text, has := p["amount"]
	if !has {
		writeError(w, http.StatusBadRequest, "amount is required")
		return
	}
	amount, err := budget.ParseAmount(text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope, project := p["scope"], p["project"]
	old, cur, err := h.Store.SetBudget(scope, project, amount)
	if err != nil {
		h.storeError(w, err)
		return
	}
	c := budgetChange{Action: "budget_set", Scope: scope, Project: project, Currency: cur.Currency,
		NewAmount: &cur.Amount, Warnings: []string{}}
	label := budgetLabel(scope, project)
	if old != nil {
		c.OldAmount = &old.Amount
		c.Message = fmt.Sprintf("budget %s set to %s %s (was %s %s)", label, formatAmount(cur.Amount), cur.Currency, formatAmount(old.Amount), old.Currency)
	} else {
		c.Message = fmt.Sprintf("budget %s set to %s %s (new)", label, formatAmount(cur.Amount), cur.Currency)
	}
	if scope == budget.ScopeProject && !budget.HasScalewayProject(h.resolved(), project) {
		c.Warnings = append(c.Warnings, fmt.Sprintf("no Scaleway project seen in this month's billing data maps to %s", project))
	}
	h.logBudget(r, c)
	writeJSON(w, http.StatusOK, c)
}

func (h *handler) unsetBudget(w http.ResponseWriter, r *http.Request) {
	p, ok := readParams(w, r)
	if !ok {
		return
	}
	scope, project := p["scope"], p["project"]
	old, err := h.Store.UnsetBudget(scope, project)
	if errors.Is(err, budget.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no budget for "+budgetLabel(scope, project))
		return
	}
	if err != nil {
		h.storeError(w, err)
		return
	}
	c := budgetChange{Action: "budget_unset", Scope: scope, Project: project, Currency: old.Currency,
		OldAmount: &old.Amount, Warnings: []string{},
		Message: fmt.Sprintf("budget %s removed (was %s %s)", budgetLabel(scope, project), formatAmount(old.Amount), old.Currency)}
	h.logBudget(r, c)
	writeJSON(w, http.StatusOK, c)
}

func (h *handler) listProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"projects": h.resolved()})
}

type mappingChange struct {
	Action      string   `json:"action"`
	ProjectID   string   `json:"project_id"`
	ProjectName string   `json:"project_name"`
	OldProject  *string  `json:"old_project"`
	OldMapping  *string  `json:"old_mapping"`
	NewProject  *string  `json:"new_project"`
	NewMapping  *string  `json:"new_mapping"`
	Message     string   `json:"message"`
	Warnings    []string `json:"warnings"`
}

// billingProject returns the project seen in billing with this ID.
func (h *handler) billingProject(id string) (scaleway.Project, bool) {
	for _, p := range h.projects() {
		if strings.ToLower(p.ID) == id {
			return p, true
		}
	}
	return scaleway.Project{}, false
}

func ptr[T any](v T) *T { return &v }

func (c *mappingChange) describe() {
	name := c.ProjectName
	if name == "" {
		name = c.ProjectID
	}
	was := "unmapped"
	if c.OldProject != nil {
		was = *c.OldProject + ", " + *c.OldMapping
	}
	if c.NewProject == nil {
		c.Message = fmt.Sprintf("Scaleway project %s has no mapping any more (was %s)", name, was)
		return
	}
	c.Message = fmt.Sprintf("Scaleway project %s now maps to %s (was %s)", name, *c.NewProject, was)
}

func (h *handler) logMapping(r *http.Request, c mappingChange) {
	h.Logger.Info("project mapping changed", "action", c.Action, "project_id", c.ProjectID,
		"old_project", logValue(c.OldProject), "old_mapping", logValue(c.OldMapping),
		"new_project", logValue(c.NewProject), "new_mapping", logValue(c.NewMapping),
		"remote_addr", r.RemoteAddr)
}

func (h *handler) setProject(w http.ResponseWriter, r *http.Request) {
	p, ok := readParams(w, r)
	if !ok {
		return
	}
	old, cur, err := h.Store.SetMapping(p["project_id"], p["project"])
	if err != nil {
		h.storeError(w, err)
		return
	}
	c := mappingChange{Action: "project_set", ProjectID: cur.ProjectID, Warnings: []string{},
		NewProject: ptr(cur.Project), NewMapping: ptr(budget.MappingManual)}
	billed, inBilling := h.billingProject(cur.ProjectID)
	c.ProjectName = billed.Name
	switch {
	case old != nil:
		c.OldProject, c.OldMapping = ptr(old.Project), ptr(budget.MappingManual)
	case inBilling:
		c.OldProject, c.OldMapping = ptr(budget.AutoProject(billed.Name, cur.ProjectID)), ptr(budget.MappingAuto)
	}
	if !inBilling {
		c.Warnings = append(c.Warnings, "this project_id is not in this month's Scaleway billing data; the mapping applies once it appears")
	}
	c.describe()
	h.logMapping(r, c)
	writeJSON(w, http.StatusOK, c)
}

func (h *handler) unsetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := readParams(w, r)
	if !ok {
		return
	}
	old, err := h.Store.UnsetMapping(p["project_id"])
	if errors.Is(err, budget.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no manual mapping for this project_id")
		return
	}
	if err != nil {
		h.storeError(w, err)
		return
	}
	c := mappingChange{Action: "project_unset", ProjectID: old.ProjectID, Warnings: []string{},
		OldProject: ptr(old.Project), OldMapping: ptr(budget.MappingManual)}
	if billed, ok := h.billingProject(old.ProjectID); ok {
		c.ProjectName = billed.Name
		c.NewProject, c.NewMapping = ptr(budget.AutoProject(billed.Name, old.ProjectID)), ptr(budget.MappingAuto)
	}
	c.describe()
	h.logMapping(r, c)
	writeJSON(w, http.StatusOK, c)
}
