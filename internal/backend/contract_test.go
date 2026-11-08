package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// locateContractOpenAPI finds the Cascade backend OpenAPI document. The
// authoritative copy lives in the HA-Contract repository; the in-repo copy
// has been removed. Set HA_CONTRACT_DIR to a HA-Contract checkout when the
// sibling-checkout candidates do not fit the local layout.
func locateContractOpenAPI(t *testing.T) string {
	t.Helper()
	const newLayoutRel = "docs/api/openapi/cascade-backend-v1.yaml"
	const legacyRel = "openapi/cascade-backend-v1.yaml"

	var candidates []string
	if dir := os.Getenv("HA_CONTRACT_DIR"); dir != "" {
		candidates = append(candidates,
			filepath.Join(dir, newLayoutRel),
			filepath.Join(dir, legacyRel),
			filepath.Join(dir, "cascade-backend-v1.yaml"),
		)
	}
	// Relative to this package (internal/backend); ../.. is the repo root.
	candidates = append(candidates,
		filepath.Join("..", "..", "HA-Contract", "openapi", "cascade-backend-v1.yaml"), // historical in-repo copy
		filepath.Join("..", "..", "..", "HA-Contract", newLayoutRel),                    // sibling checkout of HA-Contract
	)
	// Common desktop layout: the HA repositories live side by side under
	// ~/Desktop/HA (this machine's arrangement).
	if home, err := os.UserHomeDir(); err == nil {
		ha := filepath.Join(home, "Desktop", "HA", "HA-Contract")
		candidates = append(candidates,
			filepath.Join(ha, newLayoutRel),
			filepath.Join(ha, legacyRel),
		)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skipf("HA-Contract OpenAPI not found (set HA_CONTRACT_DIR to a HA-Contract checkout); tried: %s",
		strings.Join(candidates, ", "))
	return ""
}

func TestOpenAPIContractParsesAndDefinesCoreEndpoints(t *testing.T) {
	path := locateContractOpenAPI(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse OpenAPI YAML: %v", err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI version = %v, want 3.1.0", document["openapi"])
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI paths have unexpected type %T", document["paths"])
	}
	for _, path := range []string{
		"/health",
		"/rooms",
		"/sessions/{sessionID}/identity",
		"/rooms/create",
		// v1.3.0: 房间模组清单/文件只读端点 (客户端模组装配的前提)
		"/rooms/{code}/mods",
		"/rooms/{code}/mods/{file}",
	} {
		if _, exists := paths[path]; !exists {
			t.Errorf("OpenAPI contract is missing %s", path)
		}
	}
}
