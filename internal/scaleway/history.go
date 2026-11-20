package scaleway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/yagura/internal/statefile"
)

const (
	// HistoryFileName is the history file inside the state directory.
	HistoryFileName = "scaleway-history.json"

	historyVersion     = 1
	historyMaxBytes    = 8 << 20
	historyTempPattern = ".scaleway-history-*.tmp"
	periodLayout       = "2006-01"
	// retryAfter is how long a failed read waits before it is tried again.
	retryAfter = time.Hour
	// invoicesKey is the failure key of the invoice list.
	invoicesKey = "invoices"
)

// Line is the consumption of one project, category and currency in a
// billing period. ProjectName is the name the billing API gave the project
// that month.
type Line struct {
	ProjectID   string  `json:"project_id"`
	ProjectName string  `json:"project_name"`
	Category    string  `json:"category"`
	Currency    string  `json:"currency"`
	Amount      float64 `json:"amount"`
}

type monthRecord struct {
	BillingPeriod string    `json:"billing_period"`
	LastSuccess   time.Time `json:"last_success"`
	Lines         []Line    `json:"lines"`
}

type invoiceRecord struct {
	LastSuccess time.Time       `json:"last_success"`
	Amounts     []InvoiceAmount `json:"amounts"`
}

type historyFile struct {
	Version  int           `json:"version"`
	Months   []monthRecord `json:"months"`
	Invoices invoiceRecord `json:"invoices"`
}

// HistoryOptions configures OpenHistory.
type HistoryOptions struct {
	// Dir is the existing state directory; empty keeps the history in
	// memory only.
	Dir string
	// Months is the number of closed months kept, besides the current one.
	Months int
	// Interval is how old a closed month's or the invoices' last successful
	// read may get before they are read again.
	Interval time.Duration
	Now      func() time.Time // nil means time.Now
	Logger   *slog.Logger
}

// History keeps the consumption of the current month and of the last closed
// months, and the invoice totals of the closed months, with the time of each
// last successful read. It is saved to one JSON file after every change.
type History struct {
	path     string
	months   int
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	// wmu serializes saves, so that a slow write never holds mu, which every
	// scrape takes.
	wmu sync.Mutex
	mu  sync.RWMutex
	// records holds one entry per month read with success.
	records  map[string]monthRecord
	invoices invoiceRecord
	// failures holds the time of the last failed read per month, and per
	// invoicesKey; a success clears it.
	failures map[string]time.Time
	rev      uint64 // incremented on every change
	savedRev uint64
}

// OpenHistory loads the history from opts.Dir. A missing file starts an
// empty history; an unreadable, corrupt or invalid file is an error, never a
// silent reset. The directory must be writable. Months outside the window
// are dropped.
func OpenHistory(opts HistoryOptions) (*History, error) {
	h := &History{
		months:   opts.Months,
		interval: opts.Interval,
		now:      opts.Now,
		logger:   opts.Logger,
		records:  map[string]monthRecord{},
		failures: map[string]time.Time{},
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.logger == nil {
		h.logger = slog.New(slog.DiscardHandler)
	}
	if h.months < 0 || h.interval <= 0 {
		return nil, errors.New("scaleway history: months must be >= 0 and interval > 0")
	}
	if opts.Dir == "" {
		return h, nil
	}
	info, err := os.Stat(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("scaleway history: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scaleway history: %s is not a directory", opts.Dir)
	}
	h.path = filepath.Join(opts.Dir, HistoryFileName)
	statefile.RemoveLeftovers(opts.Dir, []string{historyTempPattern}, h.logger)
	f, err := loadHistory(h.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		h.logger.Info("scaleway history: no history file yet; every month will be read", "path", h.path)
	case err != nil:
		return nil, err
	default:
		for _, m := range f.Months {
			h.records[m.BillingPeriod] = m
		}
		h.invoices = f.Invoices
		if info, err := os.Stat(h.path); err == nil && info.Mode().Perm()&0o077 != 0 {
			h.logger.Warn("scaleway history: the file is readable or writable by group or others; expected 0600", "path", h.path)
		}
	}
	if err := statefile.ProbeWritable(opts.Dir); err != nil {
		return nil, fmt.Errorf("scaleway history: %w", err)
	}
	h.prune(h.now())
	return h, nil
}

func loadHistory(path string) (historyFile, error) {
	data, err := statefile.ReadRegular(path, historyMaxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return historyFile{}, err
	}
	if err != nil {
		return historyFile{}, fmt.Errorf("scaleway history: %w", err)
	}
	var f historyFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return historyFile{}, fmt.Errorf("scaleway history: %s is not valid JSON: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return historyFile{}, fmt.Errorf("scaleway history: %s has data after the JSON object", path)
	}
	if err := f.validate(); err != nil {
		return historyFile{}, fmt.Errorf("scaleway history: %s: %w", path, err)
	}
	return f, nil
}

func validPeriod(p string) bool {
	t, err := time.Parse(periodLayout, p)
	return err == nil && t.Format(periodLayout) == p
}

func validAmount(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// validate refuses what would break the exposition (duplicate series,
// invalid periods) or silently lose data (unknown version).
func (f historyFile) validate() error {
	if f.Version != historyVersion {
		return fmt.Errorf("unsupported version %d, want %d", f.Version, historyVersion)
	}
	periods := map[string]bool{}
	for _, m := range f.Months {
		if !validPeriod(m.BillingPeriod) {
			return fmt.Errorf("invalid billing_period %q", capped(m.BillingPeriod))
		}
		if periods[m.BillingPeriod] {
			return fmt.Errorf("month %s appears twice", m.BillingPeriod)
		}
		periods[m.BillingPeriod] = true
		if m.LastSuccess.IsZero() {
			return fmt.Errorf("month %s has no last_success", m.BillingPeriod)
		}
		lines := map[lineKey]bool{}
		for _, l := range m.Lines {
			k := lineKey{l.ProjectID, l.Category, l.Currency}
			if l.Currency == "" || !validAmount(l.Amount) {
				return fmt.Errorf("month %s: a line has no currency or an invalid amount", m.BillingPeriod)
			}
			if lines[k] {
				return fmt.Errorf("month %s: project %s, category %s and currency %s appear twice",
					m.BillingPeriod, capped(l.ProjectID), capped(l.Category), capped(l.Currency))
			}
			lines[k] = true
		}
	}
	amounts := map[invoiceKey]bool{}
	for _, a := range f.Invoices.Amounts {
		if !validPeriod(a.BillingPeriod) || (a.Kind != KindUntaxed && a.Kind != KindDiscount) ||
			a.Currency == "" || !validAmount(a.Amount) {
			return errors.New("invoices: an amount has an invalid period, kind, currency or value")
		}
		k := invoiceKey{a.BillingPeriod, a.Kind, a.Currency}
		if amounts[k] {
			return fmt.Errorf("invoices: %s %s %s appears twice", a.BillingPeriod, a.Kind, capped(a.Currency))
		}
		amounts[k] = true
	}
	if len(f.Invoices.Amounts) > 0 && f.Invoices.LastSuccess.IsZero() {
		return errors.New("invoices: amounts without last_success")
	}
	return nil
}

// clock returns the history's time, or the wall clock on a nil history.
func (h *History) clock() time.Time {
	if h == nil {
		return time.Now()
	}
	return h.now()
}

// period returns the UTC calendar month of t, the billing period.
func period(t time.Time) string { return t.UTC().Format(periodLayout) }

// addMonths returns the billing period n months after p (n may be negative).
func addMonths(p string, n int) string {
	t, _ := time.Parse(periodLayout, p)
	return t.AddDate(0, n, 0).Format(periodLayout)
}

// periodEnd returns the first instant after billing period p.
func periodEnd(p string) time.Time {
	t, _ := time.Parse(periodLayout, p)
	return t.AddDate(0, 1, 0)
}

// closedPeriods returns the closed months of the window at now, newest first.
func (h *History) closedPeriods(now time.Time) []string {
	cur := period(now)
	out := make([]string, 0, h.months)
	for i := 1; i <= h.months; i++ {
		out = append(out, addMonths(cur, -i))
	}
	return out
}

// inWindow reports whether p is the current month or one of the closed
// months of the window at now.
func (h *History) inWindow(p string, now time.Time) bool {
	cur := period(now)
	return p <= cur && p >= addMonths(cur, -h.months)
}

// prune drops the months, invoice amounts and failures outside the window;
// invoice amounts of the current month are dropped too.
func (h *History) prune(now time.Time) {
	cur := period(now)
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := false
	for p := range h.records {
		if !h.inWindow(p, now) {
			delete(h.records, p)
			changed = true
		}
	}
	for k := range h.failures {
		if k != invoicesKey && !h.inWindow(k, now) {
			delete(h.failures, k)
		}
	}
	kept := slices.DeleteFunc(slices.Clone(h.invoices.Amounts), func(a InvoiceAmount) bool {
		return a.BillingPeriod == cur || !h.inWindow(a.BillingPeriod, now)
	})
	if len(kept) != len(h.invoices.Amounts) {
		h.invoices.Amounts = kept
		changed = true
	}
	if changed {
		h.rev++
	}
}

// retryWait reports whether the read under key failed less than retryAfter
// before now. The caller holds mu.
func (h *History) retryWait(key string, now time.Time) bool {
	at, failed := h.failures[key]
	return failed && now.Sub(at) < retryAfter
}

// due returns the closed months to read, newest first, and whether the
// invoice list is due. A closed month is due when it was never read, was last
// read before it ended (not final yet), or was last read Interval ago or
// more; a failed read waits retryAfter.
func (h *History) due(now time.Time) ([]string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var months []string
	for _, p := range h.closedPeriods(now) {
		if h.retryWait(p, now) {
			continue
		}
		rec, ok := h.records[p]
		if !ok || rec.LastSuccess.Before(periodEnd(p)) || now.Sub(rec.LastSuccess) >= h.interval {
			months = append(months, p)
		}
	}
	invoices := h.months > 0 && !h.retryWait(invoicesKey, now) &&
		(h.invoices.LastSuccess.IsZero() || now.Sub(h.invoices.LastSuccess) >= h.interval)
	return months, invoices
}

// setMonth stores a successful read of billing period p.
func (h *History) setMonth(p string, lines []Line, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records[p] = monthRecord{BillingPeriod: p, LastSuccess: at.UTC(), Lines: lines}
	delete(h.failures, p)
	h.rev++
}

// setInvoices stores a successful read of the invoice list, keeping the
// closed months of the window only.
func (h *History) setInvoices(amounts []InvoiceAmount, at time.Time) {
	cur := period(at)
	kept := slices.DeleteFunc(slices.Clone(amounts), func(a InvoiceAmount) bool {
		return a.BillingPeriod == cur || !h.inWindow(a.BillingPeriod, at)
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invoices = invoiceRecord{LastSuccess: at.UTC(), Amounts: kept}
	delete(h.failures, invoicesKey)
	h.rev++
}

// fail records a failed read under key (a billing period or invoicesKey).
func (h *History) fail(key string, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures[key] = at
}

// failing returns the sorted keys whose last read failed.
func (h *History) failing() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	keys := make([]string, 0, len(h.failures))
	for k := range h.failures {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// observeCurrent stores the current month from a successful consumption poll
// that started at start and ended at end. A poll that crossed the month
// change is skipped: its data may belong to either month. It is safe on a
// nil history.
func (h *History) observeCurrent(start, end time.Time, totals map[groupKey]total) {
	if h == nil {
		return
	}
	p := period(end)
	if period(start) != p {
		h.logger.Info("scaleway history: the consumption poll crossed the month change; current month not updated")
		return
	}
	h.prune(end)
	h.setMonth(p, monthLines(totals), end)
	if err := h.save(); err != nil {
		h.logger.Error("scaleway history: cannot save the history; kept in memory", "error", err)
	}
}

type lineKey struct {
	projectID, category, currency string
}

// usableName reports whether name is a real project name: not empty and
// not the project ID, which the billing API uses as the name of a deleted
// project.
func usableName(name, projectID string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.EqualFold(name, projectID)
}

// monthLines sums one month's totals per project, category and currency.
// A project with several names in the month (renamed) gets the smallest
// usable one on all its lines, so that the choice does not depend on the
// order of the API rows; without one, the smallest name seen.
func monthLines(totals map[groupKey]total) []Line {
	names := map[string]string{}
	for k := range totals {
		cur, seen := names[k.projectID]
		switch {
		case !seen:
			names[k.projectID] = k.projectName
		case usableName(k.projectName, k.projectID) && (!usableName(cur, k.projectID) || k.projectName < cur):
			names[k.projectID] = k.projectName
		case !usableName(cur, k.projectID) && !usableName(k.projectName, k.projectID) && k.projectName < cur:
			names[k.projectID] = k.projectName
		}
	}
	sums := map[lineKey]total{}
	for k, t := range totals {
		lk := lineKey{k.projectID, k.category, k.currency}
		s := sums[lk]
		s.units += t.units
		s.nanos += t.nanos
		sums[lk] = s
	}
	lines := make([]Line, 0, len(sums))
	for k, t := range sums {
		lines = append(lines, Line{ProjectID: k.projectID, ProjectName: names[k.projectID],
			Category: k.category, Currency: k.currency, Amount: t.amount()})
	}
	sortLines(lines)
	return lines
}

func sortLines(lines []Line) {
	slices.SortFunc(lines, func(a, b Line) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Category, b.Category), cmp.Compare(a.Currency, b.Currency))
	})
}

// save writes the history when it changed since the last save. Without a
// state directory it does nothing.
func (h *History) save() error {
	if h.path == "" {
		return nil
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	h.mu.RLock()
	rev := h.rev
	if rev == h.savedRev {
		h.mu.RUnlock()
		return nil
	}
	f := historyFile{Version: historyVersion, Months: make([]monthRecord, 0, len(h.records)), Invoices: h.invoices}
	for _, m := range h.records {
		f.Months = append(f.Months, m)
	}
	h.mu.RUnlock()
	slices.SortFunc(f.Months, func(a, b monthRecord) int { return cmp.Compare(a.BillingPeriod, b.BillingPeriod) })
	if f.Invoices.Amounts == nil {
		f.Invoices.Amounts = []InvoiceAmount{}
	}
	for i := range f.Months {
		if f.Months[i].Lines == nil {
			f.Months[i].Lines = []Line{}
		}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("scaleway history: %w", err)
	}
	if err := statefile.WriteAtomic(h.path, historyTempPattern, append(data, '\n'), h.logger); err != nil {
		return fmt.Errorf("scaleway history: %w", err)
	}
	h.mu.Lock()
	h.savedRev = max(h.savedRev, rev)
	h.mu.Unlock()
	return nil
}

// historyView is what one scrape exposes.
type historyView struct {
	months   []monthRecord // the window's months, oldest first
	invoices []InvoiceAmount
	names    map[string]string // one name per project ID
}

// view returns the window's months at now and one name per project ID: the
// usable name of the most recent month that has one, else the project ID.
func (h *History) view(now time.Time) historyView {
	cur := period(now)
	h.mu.RLock()
	v := historyView{names: map[string]string{}}
	for p, m := range h.records {
		if h.inWindow(p, now) {
			v.months = append(v.months, m)
		}
	}
	for _, a := range h.invoices.Amounts {
		if a.BillingPeriod != cur && h.inWindow(a.BillingPeriod, now) {
			v.invoices = append(v.invoices, a)
		}
	}
	h.mu.RUnlock()
	slices.SortFunc(v.months, func(a, b monthRecord) int { return cmp.Compare(a.BillingPeriod, b.BillingPeriod) })
	for i := len(v.months) - 1; i >= 0; i-- {
		for _, l := range v.months[i].Lines {
			if _, ok := v.names[l.ProjectID]; !ok && usableName(l.ProjectName, l.ProjectID) {
				v.names[l.ProjectID] = l.ProjectName
			}
		}
	}
	for _, m := range v.months {
		for _, l := range m.Lines {
			if _, ok := v.names[l.ProjectID]; !ok {
				v.names[l.ProjectID] = l.ProjectID
			}
		}
	}
	return v
}

// Projects returns one entry per project ID of the window's months, with
// the name the monthly metrics use, sorted by ID. It is safe on a nil
// history, which lists nothing.
func (h *History) Projects() []Project {
	if h == nil {
		return nil
	}
	v := h.view(h.now())
	out := make([]Project, 0, len(v.names))
	for id, name := range v.names {
		out = append(out, Project{ID: id, Name: name})
	}
	slices.SortFunc(out, func(a, b Project) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
