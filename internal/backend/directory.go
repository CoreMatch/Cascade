package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cascade/internal/config"
)

var (
	ErrUnknownRoom  = errors.New("room is not present in the backend directory")
	ErrRoomConflict = errors.New("room is claimed by multiple backends")
	ErrBackendDown  = errors.New("backend is not healthy")
)

type BackendState struct {
	Client       *Client
	Config       config.BackendConfig
	Healthy      bool
	Failures     int
	Successes    int
	LastProbe    time.Time
	Snapshot     Snapshot
	LastSnapshot time.Time
}

type Directory struct {
	mu            sync.RWMutex
	backends      map[string]*BackendState
	defaultID     string
	healthEvery   time.Duration
	snapshotEvery time.Duration
}

func NewDirectory(cfg config.Config) (*Directory, error) {
	if cfg.DefaultBackendID == "" {
		return nil, errors.New("default_backend_id is required")
	}
	d := &Directory{
		backends:      make(map[string]*BackendState),
		defaultID:     cfg.DefaultBackendID,
		healthEvery:   time.Duration(cfg.HealthIntervalSec) * time.Second,
		snapshotEvery: time.Duration(cfg.RoomSnapshotIntervalSec) * time.Second,
	}
	if d.healthEvery <= 0 {
		d.healthEvery = 5 * time.Second
	}
	if d.snapshotEvery <= 0 {
		d.snapshotEvery = 5 * time.Second
	}
	for _, item := range cfg.Backends {
		if item.ID == "" || item.ControlURL == "" || item.GameAddress == "" {
			return nil, errors.New("each backend requires id, control_url, and game_address")
		}
		if _, exists := d.backends[item.ID]; exists {
			return nil, fmt.Errorf("duplicate backend ID %q", item.ID)
		}
		client, err := NewClient(item, cfg.BackendClientCert, cfg.BackendClientKey, cfg.BackendCAPool)
		if err != nil {
			return nil, fmt.Errorf("configure backend %s: %w", item.ID, err)
		}
		d.backends[item.ID] = &BackendState{Client: client, Config: item}
	}
	if d.backends[d.defaultID] == nil {
		return nil, fmt.Errorf("default backend %q is not configured", d.defaultID)
	}
	if err := d.validatePrefixes(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Directory) validatePrefixes() error {
	type prefixOwner struct{ prefix, id string }
	var prefixes []prefixOwner
	for id, state := range d.backends {
		for _, prefix := range state.Config.Prefixes {
			prefix = strings.ToUpper(strings.TrimSpace(prefix))
			if prefix == "" {
				return fmt.Errorf("backend %s has an empty room prefix", id)
			}
			for _, prior := range prefixes {
				if strings.HasPrefix(prefix, prior.prefix) || strings.HasPrefix(prior.prefix, prefix) {
					return fmt.Errorf("overlapping room prefixes %q (%s) and %q (%s)", prefix, id, prior.prefix, prior.id)
				}
			}
			prefixes = append(prefixes, prefixOwner{prefix: prefix, id: id})
		}
	}
	return nil
}

func (d *Directory) Start(ctx context.Context) {
	d.refreshHealth(ctx)
	d.refreshSnapshots(ctx)
	go func() {
		ticker := time.NewTicker(d.healthEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.refreshHealth(ctx)
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(d.snapshotEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.refreshSnapshots(ctx)
			}
		}
	}()
}

func (d *Directory) refreshHealth(ctx context.Context) {
	d.mu.RLock()
	states := make([]*BackendState, 0, len(d.backends))
	for _, state := range d.backends {
		states = append(states, state)
	}
	d.mu.RUnlock()

	var wg sync.WaitGroup
	for _, state := range states {
		state := state
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			healthErr := state.Client.Health(probeCtx)
			now := time.Now().UTC()
			d.mu.Lock()
			recordHealthResult(state, healthErr, now)
			d.mu.Unlock()

		}()
	}
	wg.Wait()
}

func recordHealthResult(state *BackendState, probeErr error, at time.Time) {
	state.LastProbe = at
	if probeErr != nil {
		state.Failures++
		state.Successes = 0
		if state.Failures >= 3 {
			state.Healthy = false
		}
		return
	}
	state.Failures = 0
	state.Successes++
	if state.Successes >= 2 {
		state.Healthy = true
	}
}

func (d *Directory) refreshSnapshots(ctx context.Context) {
	d.mu.RLock()
	states := make([]*BackendState, 0, len(d.backends))
	for _, state := range d.backends {
		states = append(states, state)
	}
	d.mu.RUnlock()

	var wg sync.WaitGroup
	for _, state := range states {
		state := state
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshotCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			snapshot, err := state.Client.Rooms(snapshotCtx)
			if err != nil {
				return
			}
			d.mu.Lock()
			state.Snapshot = snapshot
			state.LastSnapshot = time.Now().UTC()
			d.mu.Unlock()
		}()
	}
	wg.Wait()
}

func (d *Directory) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	state := d.backends[d.defaultID]
	return state != nil && state.Healthy
}

func (d *Directory) Default() (*Client, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	state := d.backends[d.defaultID]
	if state == nil {
		return nil, errors.New("default backend is not configured")
	}
	if !state.Healthy {
		return nil, ErrBackendDown
	}
	return state.Client, nil
}

func (d *Directory) RouteRoom(code string) (*Client, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	d.mu.RLock()
	defer d.mu.RUnlock()
	owner := ""
	for id, state := range d.backends {
		for _, room := range state.Snapshot.Rooms {
			if strings.EqualFold(room.Code, code) {
				if owner != "" && owner != id {
					return nil, ErrRoomConflict
				}
				owner = id
			}
		}
	}
	if owner == "" {
		bestLen := -1
		for id, state := range d.backends {
			for _, prefix := range state.Config.Prefixes {
				prefix = strings.ToUpper(strings.TrimSpace(prefix))
				if strings.HasPrefix(code, prefix) && len(prefix) > bestLen {
					owner = id
					bestLen = len(prefix)
				}
			}
		}
	}
	if owner == "" {
		owner = d.defaultID
	}
	state := d.backends[owner]
	if state == nil {
		return nil, errors.New("room route references an unknown backend")
	}
	if !state.Healthy {
		return nil, ErrBackendDown
	}
	return state.Client, nil
}

func (d *Directory) RouteInitial(code string) (*Client, error) {
	if strings.TrimSpace(code) == "" {
		return d.Default()
	}
	return d.RouteRoom(code)
}

func (d *Directory) SnapshotRooms() []Room {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rooms := make([]Room, 0)
	for _, state := range d.backends {
		for _, room := range state.Snapshot.Rooms {
			room.Members = nil
			rooms = append(rooms, room)
		}
	}
	sort.Slice(rooms, func(i, j int) bool {
		return strings.ToUpper(rooms[i].Code) < strings.ToUpper(rooms[j].Code)
	})
	return rooms
}

func (d *Directory) Statuses() []Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	items := make([]Status, 0, len(d.backends))
	for id, state := range d.backends {
		items = append(items, Status{
			ID: id, Healthy: state.Healthy, Failures: state.Failures,
			Successes: state.Successes, LastProbe: state.LastProbe,
			LastSnapshot: state.LastSnapshot, RoomCount: len(state.Snapshot.Rooms),
			ControlURL: state.Client.ControlURL, GameAddress: state.Client.GameAddress,
			ConflictRooms: d.conflictsForBackendLocked(id),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (d *Directory) conflictsForBackendLocked(backendID string) []string {
	owners := make(map[string]map[string]struct{})
	for id, state := range d.backends {
		for _, room := range state.Snapshot.Rooms {
			code := strings.ToUpper(room.Code)
			if owners[code] == nil {
				owners[code] = make(map[string]struct{})
			}
			owners[code][id] = struct{}{}
		}
	}
	var conflicts []string
	for code, ids := range owners {
		if _, belongs := ids[backendID]; belongs && len(ids) > 1 {
			conflicts = append(conflicts, code)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}

func (d *Directory) BackendHealthy(id string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	state := d.backends[id]
	return state != nil && state.Healthy
}

func (d *Directory) Client(id string) (*Client, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	state, ok := d.backends[id]
	if !ok {
		return nil, false
	}
	return state.Client, true
}

func (d *Directory) SnapshotInterval() time.Duration {
	return d.snapshotEvery
}
