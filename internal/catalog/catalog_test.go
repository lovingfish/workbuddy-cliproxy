package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixture = `{"code":0,"data":{
 "models":[
  {"id":"hy3","name":"Hy3","credits":"x0.00 credits","reasoning":{"effort":"high"},"supportsReasoning":true,"accessToken":"DO_NOT_EXPOSE"},
  {"id":"glm-5.3-flash","name":"GLM-5.3-Flash","credits":"x0.06 credits","reasoning":{"defaultEffort":"high","supportedEfforts":["low","high","max"],"canDisableThinking":true}},
  {"id":"hy4-preview-f","name":"Hy4 preview","credits":"x0.00 credits","reasoning":{"defaultEffort":"high","supportedEfforts":["high"],"canDisableThinking":false}},
  {"id":"blocked","disabled":true},
  {"id":"other-channel-only","name":"Not CLI"}
 ],
 "agents":[{"name":"cli","models":["hy3","glm-5.3-flash","hy4-preview-f","blocked","missing"]},{"name":"craft","models":["other-channel-only"]}],
 "modelPromotions":[{"id":"promo","modelIds":["hy4-preview-f"],"enabled":true,"discount":{"factor":0},"accessToken":"DO_NOT_EXPOSE"}]
}}`

func TestParseChannelAndPublicFields(t *testing.T) {
	now := time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC)
	snapshot, err := Parse([]byte(fixture), "cli", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 3 {
		t.Fatalf("models=%d", len(snapshot.Models))
	}
	model, _ := snapshot.Find("glm-5.3-flash")
	if len(model.SupportedEfforts()) != 3 {
		t.Fatal("reasoning levels were lost")
	}
	fields := snapshot.PublicFields(model, now)
	if fields["credits"] != "x0.06 credits" || fields["display_name"] != "GLM-5.3-Flash" {
		t.Fatal(fields)
	}
	hy3, _ := snapshot.Find("hy3")
	if _, ok := snapshot.PublicFields(hy3, now)["supported_reasoning_efforts"]; ok {
		t.Fatal("default effort invented a supported set")
	}
	hy4, _ := snapshot.Find("hy4-preview-f")
	raw, _ := json.Marshal(snapshot.PublicFields(hy4, now))
	if !strings.Contains(string(raw), "promo") || strings.Contains(string(raw), "DO_NOT_EXPOSE") {
		t.Fatalf("unsafe or missing metadata: %s", raw)
	}
	if _, err := Parse([]byte(fixture), "unknown", now); err == nil {
		t.Fatal("missing channel accepted")
	}
}
func TestParseEmptyChannelIsAuthoritative(t *testing.T) {
	snapshot, err := Parse([]byte(`{"code":0,"data":{"models":[{"id":"old"}],"agents":[{"name":"cli","models":[]}]}}`), "cli", time.Now())
	if err != nil || len(snapshot.Models) != 0 {
		t.Fatalf("snapshot=%v err=%v", snapshot, err)
	}
}
func TestReasoningPreservesExplicitChoiceAndValidatesPublishedLevels(t *testing.T) {
	snapshot, _ := Parse([]byte(fixture), "cli", time.Now())
	hy3, _ := snapshot.Find("hy3")
	body := map[string]any{"reasoning_effort": "low"}
	if err := hy3.ApplyReasoning(body); err != nil || body["reasoning_effort"] != "low" {
		t.Fatalf("hy3 low overwritten: %v %v", body, err)
	}
	body = map[string]any{}
	if err := hy3.ApplyReasoning(body); err != nil || body["reasoning_effort"] != "high" {
		t.Fatal("provider default was not used")
	}
	glm, _ := snapshot.Find("glm-5.3-flash")
	if err := glm.ApplyReasoning(map[string]any{"reasoning_effort": "xhigh"}); err == nil {
		t.Fatal("unsupported level accepted")
	}
	body = map[string]any{"thinking": map[string]any{"type": "disabled"}}
	if err := glm.ApplyReasoning(body); err != nil || body["reasoning_effort"] != "none" {
		t.Fatal("explicit disable was overwritten")
	}
	hy4, _ := snapshot.Find("hy4-preview-f")
	if err := hy4.ApplyReasoning(map[string]any{"reasoning_effort": "none"}); err == nil {
		t.Fatal("forbidden disable accepted")
	}
}
func TestManagerScopesCacheAndRefreshesWithoutSleep(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	calls := 0
	manager := NewManager(func(context.Context, Credentials) ([]byte, error) { calls++; return []byte(fixture), nil }, Options{TTL: time.Minute})
	manager.now = func() time.Time { return now }
	a := Credentials{UID: "a", AccessToken: "token-a"}
	b := Credentials{UID: "b", AccessToken: "token-b"}
	first, err := manager.Get(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	first.Models[0].Name = "corrupt"
	second, _ := manager.Get(context.Background(), a)
	if calls != 1 || second.Models[0].Name == "corrupt" {
		t.Fatal("cache miss or mutable snapshot")
	}
	_, _ = manager.Get(context.Background(), b)
	if calls != 2 {
		t.Fatal("account caches were mixed")
	}
	a.AccessToken = "rotated"
	_, _ = manager.Get(context.Background(), a)
	if calls != 3 {
		t.Fatal("credential rotation did not refresh catalog")
	}
	now = now.Add(2 * time.Minute)
	_, _ = manager.Get(context.Background(), a)
	if calls != 4 {
		t.Fatal("TTL did not refresh")
	}
	manager.Reconfigure(Options{Channel: "craft", TTL: time.Minute})
	alternate, err := manager.Get(context.Background(), a)
	if err != nil || len(alternate.Models) != 1 || alternate.Models[0].ID != "other-channel-only" {
		t.Fatal("channel cache was reused")
	}
}
func TestManagerRetainsBoundedLastGoodCatalogButNotUnauthorized(t *testing.T) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	var failure error
	calls := 0
	manager := NewManager(func(context.Context, Credentials) ([]byte, error) {
		calls++
		if failure != nil {
			return nil, failure
		}
		return []byte(fixture), nil
	}, Options{TTL: time.Minute, MaxStale: 3 * time.Minute})
	manager.now = func() time.Time { return now }
	auth := Credentials{UID: "a", AccessToken: "t"}
	_, _ = manager.Get(context.Background(), auth)
	now = now.Add(2 * time.Minute)
	failure = errors.New("network")
	cached, err := manager.Get(context.Background(), auth)
	if err != nil || !cached.Stale {
		t.Fatal("last-good cache not preserved")
	}
	_, _ = manager.Get(context.Background(), auth)
	if calls != 2 {
		t.Fatal("failure retry backoff not enforced")
	}
	now = now.Add(2 * time.Minute)
	if _, err := manager.Get(context.Background(), auth); err == nil {
		t.Fatal("stale cache never expires")
	}
	failure = nil
	manager.Reconfigure(Options{TTL: time.Minute})
	_, _ = manager.Get(context.Background(), auth)
	failure = &HTTPError{Status: 401}
	manager.Reconfigure(Options{TTL: time.Minute})
	if _, err := manager.Get(context.Background(), auth); err == nil {
		t.Fatal("unauthorized reused cached capabilities")
	}
}
func TestManagerDeduplicatesConcurrentDiscovery(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(func(context.Context, Credentials) ([]byte, error) { calls.Add(1); return []byte(fixture), nil }, Options{})
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, err := manager.Get(context.Background(), Credentials{UID: "a", AccessToken: "t"}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fetches=%d", calls.Load())
	}
}
func TestHTTPFetcherUsesCurrentAccountHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-User-Id") != "test-user" || r.Header.Get("X-Enterprise-Id") != "test-enterprise" || r.Header.Get("X-Product") != "SaaS" {
			t.Error("account or channel headers missing")
		}
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	fetch := HTTPFetcher(server.Client(), server.URL, "CLI/test")
	raw, err := fetch(context.Background(), Credentials{AccessToken: "test-token", UID: "test-user", EnterpriseID: "test-enterprise"})
	if err != nil || string(raw) != fixture {
		t.Fatal(err)
	}
}
