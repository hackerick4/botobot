package server

import (
	"net/http"
	"testing"
)

// /healthz reports the highest applied migration so a deploy can confirm its
// migrations ran (v0.4.8); it carries no data.
func TestHealthzSchemaVersion(t *testing.T) {
	ts := newIntegServer(t, nil)
	status, body, _ := ts.do(t, http.MethodGet, "/healthz", nil, reqOpts{})
	if status != http.StatusOK || body["ok"] != true {
		t.Fatalf("healthz = %d %v", status, body)
	}
	if v, _ := body["schema_version"].(float64); v < 6 {
		t.Fatalf("schema_version = %v, want >= 6", body["schema_version"])
	}
	if len(body) != 2 {
		t.Errorf("healthz should carry only ok and schema_version: %v", body)
	}
}
