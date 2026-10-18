// Package mod manages game mod definitions — unit metadata that clients need
// to load the correct mod resources when joining a room.
//
// In relay mode the server only needs unit names and IDs to populate the
// SERVER_INFO packet's "customUnits" block.  In future server mode, UnitDef
// can be extended with gameplay attributes (health, speed, build cost, etc.)
// so the server can validate commands and simulate the game.
package mod

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// UnitDef describes a single unit type provided by a mod.
// This is the minimal metadata needed for relay mode.
// Future server-mode fields (HP, speed, cost, …) can be added here
// without changing the loading or encoding logic.
type UnitDef struct {
	ModName  string `json:"modName"`
	UnitName string `json:"unitName"`
	UnitID   int32  `json:"unitId"`
}

// Mod is a named collection of unit definitions loaded from one JSON file.
type Mod struct {
	Name  string
	Units []UnitDef
}

// Registry holds all loaded mods and provides lookup helpers.
// It is safe for concurrent reads after loading completes.
type Registry struct {
	mu   sync.RWMutex
	mods map[string]*Mod // keyed by mod name
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{mods: make(map[string]*Mod)}
}

// LoadDir scans dir for JSON files whose names start with "mod_" and loads
// each one as a Mod.  Files that fail to parse are logged and skipped.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no mods directory is fine
		}
		return fmt.Errorf("read mods dir: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "mod_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		if err := r.loadFileLocked(path); err != nil {
			log.Printf("[mod] skip %s: %v", path, err)
		}
	}
	return nil
}

// LoadFile loads a single mod JSON file into the registry.
func (r *Registry) LoadFile(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadFileLocked(path)
}

func (r *Registry) loadFileLocked(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var units []UnitDef
	if err := json.Unmarshal(data, &units); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	if len(units) == 0 {
		return fmt.Errorf("no units defined")
	}

	modName := units[0].ModName
	if modName == "" {
		// Derive mod name from filename: mod_foo.json → foo
		base := filepath.Base(path)
		modName = strings.TrimPrefix(base, "mod_")
		modName = strings.TrimSuffix(modName, ".json")
		for i := range units {
			units[i].ModName = modName
		}
	}

	r.mods[modName] = &Mod{Name: modName, Units: units}
	log.Printf("[mod] loaded %q with %d units from %s", modName, len(units), path)
	return nil
}

// AllUnits returns a flat slice of every unit across all loaded mods.
// This is used to populate the SERVER_INFO "customUnits" block.
func (r *Registry) AllUnits() []UnitDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var all []UnitDef
	for _, m := range r.mods {
		all = append(all, m.Units...)
	}
	return all
}

// ModNames returns the names of all loaded mods.
func (r *Registry) ModNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.mods))
	for name := range r.mods {
		names = append(names, name)
	}
	return names
}

// GetMod returns a mod by name, or nil if not loaded.
func (r *Registry) GetMod(name string) *Mod {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mods[name]
}

// Count returns the number of loaded mods.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.mods)
}