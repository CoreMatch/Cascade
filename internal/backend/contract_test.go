package backend

import (
	"os"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestOpenAPIContractParsesAndDefinesCoreEndpoints(t *testing.T) {
	data, err := os.ReadFile("../../HA-Contract/openapi/mcags-rw-backend-v1.yaml")
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
	} {
		if _, exists := paths[path]; !exists {
			t.Errorf("OpenAPI contract is missing %s", path)
		}
	}
}
