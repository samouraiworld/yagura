package scaleway

import (
	"slices"
	"strings"
	"sync"
)

// Project is a Scaleway project seen in the billing consumption data.
type Project struct {
	ID   string
	Name string
}

// Catalog holds the projects of the last successful consumption poll. It
// needs no Scaleway call or permission beyond the billing API: a project
// with no consumption in the current month is absent.
type Catalog struct {
	mu       sync.RWMutex
	projects []Project
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog { return &Catalog{} }

// List returns a copy of the projects, sorted by ID. It is safe on a nil
// catalog, which lists nothing.
func (c *Catalog) List() []Project {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.projects)
}

// set replaces the projects with one entry per non-empty ID of keys. When
// an ID comes with several names (a project renamed during the month), the
// smallest non-empty name wins, so that the choice does not depend on the
// order of the API rows.
func (c *Catalog) set(keys []groupKey) {
	if c == nil {
		return
	}
	names := map[string]string{}
	for _, k := range keys {
		if k.projectID == "" {
			continue
		}
		cur, seen := names[k.projectID]
		if !seen || cur == "" || (k.projectName != "" && k.projectName < cur) {
			names[k.projectID] = k.projectName
		}
	}
	projects := make([]Project, 0, len(names))
	for id, name := range names {
		projects = append(projects, Project{ID: id, Name: name})
	}
	slices.SortFunc(projects, func(a, b Project) int { return strings.Compare(a.ID, b.ID) })
	c.mu.Lock()
	c.projects = projects
	c.mu.Unlock()
}
