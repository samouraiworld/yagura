package budget

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/yagura/internal/statefile"
)

const (
	// FileName is the store file inside the state directory.
	FileName = "budgets.json"
	// MaxEntries caps the budgets and the manual mappings, each, so that
	// the API cannot grow the metric cardinality without bound.
	MaxEntries = 500

	fileVersion  = 1
	maxFileBytes = 1 << 20
	tempPattern  = ".budgets-*.tmp"
)

var (
	// ErrNotFound is returned when unsetting an entry that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrLimit is returned when a new entry would pass MaxEntries.
	ErrLimit = errors.New("too many entries")
)

// InvalidError reports an input that fails validation.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &InvalidError{msg: fmt.Sprintf(format, args...)}
}

// Budget is a monthly budget, excluding VAT.
type Budget struct {
	Scope     string    `json:"scope"`
	Project   string    `json:"project"` // empty unless Scope is ScopeProject
	Amount    float64   `json:"amount"`
	Currency  string    `json:"currency"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Mapping is a manual mapping of a Scaleway project to a business project.
type Mapping struct {
	ProjectID string    `json:"project_id"`
	Project   string    `json:"project"`
	UpdatedAt time.Time `json:"updated_at"`
}

type state struct {
	Version  int       `json:"version"`
	Budgets  []Budget  `json:"budgets"`
	Mappings []Mapping `json:"project_mappings"`
}

// Options configures Open.
type Options struct {
	Dir string // existing state directory
	// InitialTotal and InitialScaleway seed the total and scaleway budgets
	// when the store file does not exist; 0 means no seed.
	InitialTotal    float64
	InitialScaleway float64
	// RequireWritable makes Open fail when the directory is not writable
	// (the write API is on).
	RequireWritable bool
	Now             func() time.Time // nil means time.Now
	Logger          *slog.Logger
}

// Store keeps budgets and manual mappings in one JSON file. Every change is
// written atomically before it shows in memory.
type Store struct {
	path       string
	now        func() time.Time
	logger     *slog.Logger
	maxEntries int

	// wmu serializes writers, so that a slow file write never holds mu,
	// which every scrape takes to read the budgets.
	wmu sync.Mutex
	mu  sync.RWMutex
	st  state
}

// Open loads the store from opts.Dir, creating it with the seeds when the
// file does not exist. An unreadable, corrupt or invalid file is an error,
// never a silent reset.
func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("budget store: state directory is empty")
	}
	info, err := os.Stat(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("budget store: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("budget store: %s is not a directory", opts.Dir)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if info.Mode().Perm()&0o022 != 0 {
		logger.Warn("budget store: the state directory is writable by group or others", "path", opts.Dir)
	}
	s := &Store{path: filepath.Join(opts.Dir, FileName), now: opts.Now, logger: logger, maxEntries: MaxEntries}
	if s.now == nil {
		s.now = time.Now
	}
	statefile.RemoveLeftovers(opts.Dir, []string{tempPattern, statefile.ProbePattern}, logger)
	seeded := opts.InitialTotal > 0 || opts.InitialScaleway > 0

	switch st, err := load(s.path); {
	case errors.Is(err, fs.ErrNotExist):
		st := state{Version: fileVersion}
		now := s.now().UTC()
		for _, seed := range []struct {
			scope  string
			amount float64
		}{{ScopeTotal, opts.InitialTotal}, {ScopeScaleway, opts.InitialScaleway}} {
			if seed.amount == 0 {
				continue
			}
			if !validAmount(seed.amount) {
				return nil, fmt.Errorf("budget store: initial %s budget: %w", seed.scope, ErrAmount)
			}
			st.Budgets = append(st.Budgets, Budget{Scope: seed.scope, Amount: seed.amount, Currency: Currency, UpdatedAt: now})
		}
		if err := s.save(st); err != nil {
			return nil, err
		}
		s.st = st
		logger.Info("budget store created", "path", s.path,
			"initial_total", opts.InitialTotal, "initial_scaleway", opts.InitialScaleway)
		return s, nil
	case err != nil:
		return nil, err
	default:
		s.st = st
		if info, err := os.Stat(s.path); err == nil && info.Mode().Perm()&0o077 != 0 {
			logger.Warn("budget store: the file is readable or writable by group or others; expected 0600", "path", s.path)
		}
	}
	if seeded {
		logger.Info("budget seeds ignored: the store already exists", "path", s.path)
	}
	if opts.RequireWritable {
		if err := statefile.ProbeWritable(opts.Dir); err != nil {
			return nil, fmt.Errorf("budget store: %w", err)
		}
	}
	return s, nil
}

// load reads and validates the store file.
func load(path string) (state, error) {
	data, err := statefile.ReadRegular(path, maxFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, err
	}
	if err != nil {
		return state{}, fmt.Errorf("budget store: %w", err)
	}
	var st state
	dec := json.NewDecoder(bytes.NewReader(data))
	// A misspelled key would otherwise load as an empty section, and the
	// next change would drop its data from the file.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return state{}, fmt.Errorf("budget store: %s is not valid JSON: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return state{}, fmt.Errorf("budget store: %s has data after the JSON object", path)
	}
	if err := st.validate(); err != nil {
		return state{}, fmt.Errorf("budget store: %s: %w", path, err)
	}
	st.sort()
	return st, nil
}

// sort orders the entries the way the file and the API list them.
func (st *state) sort() {
	slices.SortFunc(st.Budgets, func(a, b Budget) int {
		return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Project, b.Project))
	})
	slices.SortFunc(st.Mappings, func(a, b Mapping) int { return cmp.Compare(a.ProjectID, b.ProjectID) })
}

func (st state) validate() error {
	if st.Version != fileVersion {
		return fmt.Errorf("unsupported version %d, want %d", st.Version, fileVersion)
	}
	if len(st.Budgets) > MaxEntries || len(st.Mappings) > MaxEntries {
		return fmt.Errorf("more than %d budgets or mappings", MaxEntries)
	}
	seen := map[string]bool{}
	for _, b := range st.Budgets {
		if err := validateBudgetKey(b.Scope, b.Project); err != nil {
			return err
		}
		if !validAmount(b.Amount) {
			return fmt.Errorf("budget %s %s: %w", b.Scope, b.Project, ErrAmount)
		}
		if b.Currency != Currency {
			return fmt.Errorf("budget %s %s: currency must be %s", b.Scope, b.Project, Currency)
		}
		k := b.Scope + "/" + b.Project
		if seen[k] {
			return fmt.Errorf("budget %s %s appears twice", b.Scope, b.Project)
		}
		seen[k] = true
	}
	for _, m := range st.Mappings {
		if id, ok := NormalizeProjectID(m.ProjectID); !ok || id != m.ProjectID {
			return errors.New("mapping: project_id must be a lowercase UUID")
		}
		if !ValidProject(m.Project) {
			return fmt.Errorf("mapping %s: invalid project", m.ProjectID)
		}
		if seen[m.ProjectID] {
			return fmt.Errorf("mapping %s appears twice", m.ProjectID)
		}
		seen[m.ProjectID] = true
	}
	return nil
}

// save writes st atomically (see statefile.WriteAtomic).
func (s *Store) save(st state) error {
	st.sort()
	if st.Budgets == nil {
		st.Budgets = []Budget{}
	}
	if st.Mappings == nil {
		st.Mappings = []Mapping{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("budget store: %w", err)
	}
	if err := statefile.WriteAtomic(s.path, tempPattern, append(data, '\n'), s.logger); err != nil {
		return fmt.Errorf("budget store: %w", err)
	}
	return nil
}

func validateBudgetKey(scope, project string) error {
	switch {
	case !ValidScope(scope):
		return invalid("scope must be one of total, scaleway, project")
	case scope == ScopeProject && project == "":
		return invalid("project is required when scope is project")
	case scope != ScopeProject && project != "":
		return invalid("project is only allowed when scope is project")
	case scope == ScopeProject && !ValidProject(project):
		return invalid("project must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	return nil
}

// Budgets returns a sorted copy of the budgets.
func (s *Store) Budgets() []Budget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.st.Budgets)
}

// Mappings returns a sorted copy of the manual mappings.
func (s *Store) Mappings() []Mapping {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.st.Mappings)
}

// update applies change to a copy of the state, saves it, then swaps it in.
func (s *Store) update(change func(st *state) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.RLock()
	st := state{
		Version:  s.st.Version,
		Budgets:  slices.Clone(s.st.Budgets),
		Mappings: slices.Clone(s.st.Mappings),
	}
	s.mu.RUnlock()
	if err := change(&st); err != nil {
		return err
	}
	if err := s.save(st); err != nil {
		return err
	}
	st.sort()
	s.mu.Lock()
	s.st = st
	s.mu.Unlock()
	return nil
}

// SetBudget creates or changes a budget. It returns the previous budget, nil
// when there was none, and the new one.
func (s *Store) SetBudget(scope, project string, amount float64) (*Budget, Budget, error) {
	if err := validateBudgetKey(scope, project); err != nil {
		return nil, Budget{}, err
	}
	if !validAmount(amount) {
		return nil, Budget{}, &InvalidError{msg: ErrAmount.Error()}
	}
	var old *Budget
	cur := Budget{Scope: scope, Project: project, Amount: amount, Currency: Currency, UpdatedAt: s.now().UTC()}
	err := s.update(func(st *state) error {
		i := slices.IndexFunc(st.Budgets, func(b Budget) bool { return b.Scope == scope && b.Project == project })
		if i >= 0 {
			prev := st.Budgets[i]
			old = &prev
			st.Budgets[i] = cur
			return nil
		}
		if len(st.Budgets) >= s.maxEntries {
			return fmt.Errorf("%w: at most %d budgets", ErrLimit, s.maxEntries)
		}
		st.Budgets = append(st.Budgets, cur)
		return nil
	})
	if err != nil {
		return nil, Budget{}, err
	}
	return old, cur, nil
}

// UnsetBudget removes a budget and returns it, or ErrNotFound.
func (s *Store) UnsetBudget(scope, project string) (Budget, error) {
	if err := validateBudgetKey(scope, project); err != nil {
		return Budget{}, err
	}
	var old Budget
	err := s.update(func(st *state) error {
		i := slices.IndexFunc(st.Budgets, func(b Budget) bool { return b.Scope == scope && b.Project == project })
		if i < 0 {
			return ErrNotFound
		}
		old = st.Budgets[i]
		st.Budgets = slices.Delete(st.Budgets, i, i+1)
		return nil
	})
	return old, err
}

// SetMapping maps a Scaleway project ID to a business project, overriding
// the automatic rule. It returns the previous manual mapping, nil when there
// was none, and the new one.
func (s *Store) SetMapping(projectID, project string) (*Mapping, Mapping, error) {
	id, ok := NormalizeProjectID(projectID)
	if !ok {
		return nil, Mapping{}, invalid("project_id must be a UUID")
	}
	if !ValidProject(project) {
		return nil, Mapping{}, invalid("project must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	var old *Mapping
	cur := Mapping{ProjectID: id, Project: project, UpdatedAt: s.now().UTC()}
	err := s.update(func(st *state) error {
		i := slices.IndexFunc(st.Mappings, func(m Mapping) bool { return m.ProjectID == id })
		if i >= 0 {
			prev := st.Mappings[i]
			old = &prev
			st.Mappings[i] = cur
			return nil
		}
		if len(st.Mappings) >= s.maxEntries {
			return fmt.Errorf("%w: at most %d project mappings", ErrLimit, s.maxEntries)
		}
		st.Mappings = append(st.Mappings, cur)
		return nil
	})
	if err != nil {
		return nil, Mapping{}, err
	}
	return old, cur, nil
}

// UnsetMapping removes a manual mapping and returns it, or ErrNotFound.
func (s *Store) UnsetMapping(projectID string) (Mapping, error) {
	id, ok := NormalizeProjectID(projectID)
	if !ok {
		return Mapping{}, invalid("project_id must be a UUID")
	}
	var old Mapping
	err := s.update(func(st *state) error {
		i := slices.IndexFunc(st.Mappings, func(m Mapping) bool { return m.ProjectID == id })
		if i < 0 {
			return ErrNotFound
		}
		old = st.Mappings[i]
		st.Mappings = slices.Delete(st.Mappings, i, i+1)
		return nil
	})
	return old, err
}
