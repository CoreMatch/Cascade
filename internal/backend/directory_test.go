package backend

import (
	"errors"
	"testing"
	"time"

	"cascade/internal/config"
)

func TestRouteRoomUsesSnapshotThenPrefixThenDefault(t *testing.T) {
	a := &Client{ID: "a", ControlURL: "https://a"}
	b := &Client{ID: "b", ControlURL: "https://b"}
	d := &Directory{
		defaultID: "a",
		backends: map[string]*BackendState{
			"a": {Client: a, Healthy: true, Config: config.BackendConfig{ID: "a", Prefixes: []string{"A"}}, Snapshot: Snapshot{Rooms: []Room{{Code: "A123"}}}},
			"b": {Client: b, Healthy: true, Config: config.BackendConfig{ID: "b", Prefixes: []string{"B"}}},
		},
	}
	for code, expected := range map[string]string{"A123": "a", "B999": "b", "X999": "a"} {
		client, err := d.RouteRoom(code)
		if err != nil {
			t.Fatalf("route %s: %v", code, err)
		}
		if client.ID != expected {
			t.Errorf("route %s = %s, want %s", code, client.ID, expected)
		}
	}
}

func TestRouteRejectsConflictingRoomOwnership(t *testing.T) {
	d := &Directory{
		defaultID: "a",
		backends: map[string]*BackendState{
			"a": {Client: &Client{ID: "a"}, Healthy: true, Snapshot: Snapshot{Rooms: []Room{{Code: "DUP"}}}},
			"b": {Client: &Client{ID: "b"}, Healthy: true, Snapshot: Snapshot{Rooms: []Room{{Code: "DUP"}}}},
		},
	}
	if _, err := d.RouteRoom("DUP"); !errors.Is(err, ErrRoomConflict) {
		t.Fatalf("route error = %v, want ErrRoomConflict", err)
	}
}

func TestValidatePrefixesRejectsOverlap(t *testing.T) {
	d := &Directory{backends: map[string]*BackendState{
		"a": {Config: config.BackendConfig{Prefixes: []string{"RW"}}},
		"b": {Config: config.BackendConfig{Prefixes: []string{"RW-EU"}}},
	}}
	if err := d.validatePrefixes(); err == nil {
		t.Fatal("expected overlapping prefixes to fail")
	}
}

func TestSnapshotInterval(t *testing.T) {
	d := &Directory{snapshotEvery: 5 * time.Second}
	if d.SnapshotInterval() != 5*time.Second {
		t.Fatal("snapshot interval not preserved")
	}
}

func TestBackendHealthThresholdsAndRecovery(t *testing.T) {
	state := &BackendState{}
	now := time.Now().UTC()

	for i := 0; i < 2; i++ {
		recordHealthResult(state, errors.New("probe failed"), now.Add(time.Duration(i)*time.Second))
	}
	if state.Healthy {
		t.Fatal("backend became unhealthy before three consecutive failures")
	}
	recordHealthResult(state, errors.New("probe failed"), now.Add(2*time.Second))
	if state.Healthy || state.Failures != 3 {
		t.Fatalf("backend health after three failures: %+v", state)
	}

	recordHealthResult(state, nil, now.Add(3*time.Second))
	if state.Healthy {
		t.Fatal("backend recovered before two consecutive successes")
	}
	recordHealthResult(state, nil, now.Add(4*time.Second))
	if !state.Healthy || state.Successes != 2 || state.Failures != 0 {
		t.Fatalf("backend did not recover after two successes: %+v", state)
	}
}
