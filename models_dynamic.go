package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Discover additional, account-visible models. Never store or log credentials.
// Both endpoints are undocumented: a cached, known-good catalog is used on failure.
var catalogCache = struct {
	sync.Mutex
	data map[string]cachedModels
}{data: map[string]cachedModels{}}

type cachedModels struct {
	models  []pluginapi.ModelInfo
	expires time.Time
}

func modelsForAuth(raw []byte) []pluginapi.ModelInfo {
	fallback := wbModels()
	var req pluginapi.AuthModelRequest
	if json.Unmarshal(raw, &req) != nil || len(req.StorageJSON) == 0 {
		return fallback
	}
	auth, err := parseStored(req.StorageJSON)
	if err != nil || auth.Auth.AccessToken == "" {
		return fallback
	}
	cacheKey := auth.Account.UID + ":" + auth.Auth.Domain
	if cacheKey == ":" {
		return fallback
	}
	catalogCache.Lock()
	cached, hit := catalogCache.data[cacheKey]
	if hit && time.Now().Before(cached.expires) {
		catalogCache.Unlock()
		return combineModels(fallback, cached.models)
	}
	catalogCache.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Second)
	defer cancel()
	dynamic := discoverModels(ctx, auth)
	if len(dynamic) == 0 {
		// A transient upstream failure must not wipe an earlier successful catalog.
		if hit {
			return combineModels(fallback, cached.models)
		}
		return fallback
	}
	catalogCache.Lock()
	catalogCache.data[cacheKey] = cachedModels{models: dynamic, expires: time.Now().Add(30 * time.Minute)}
	catalogCache.Unlock()
	return combineModels(fallback, dynamic)
}

func combineModels(primary, secondary []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	result := append([]pluginapi.ModelInfo{}, primary...)
	seen := make(map[string]bool, len(result))
	for _, m := range result {
		seen[m.ID] = true
	}
	for _, m := range secondary {
		if !seen[m.ID] {
			result = append(result, m)
			seen[m.ID] = true
		}
	}
	return result
}

func discoverModels(ctx context.Context, auth *storedAuth) []pluginapi.ModelInfo {
	// User agents affect the model catalog returned by /v3/config.
	routes := []struct{ path, ua string }{
		{"/v3/config", "WorkBuddy/5.5.4 WorkBuddy AI/5.5.4 CLI/2.137.1"},
		{"/v3/config", "CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0"},
		{"/console/enterprises/personal/models", clientUA},
		{"/v2/enterprises/personal/models", clientUA},
	}
	// Concurrent probes prevent four timeout delays from stacking.
	type response struct{ models []pluginapi.ModelInfo }
	result := make(chan response, len(routes))
	for _, route := range routes {
		route := route
		go func() {
			url := upstreamBase + route.path
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				result <- response{}
				return
			}
			backendHeaders(req, auth)
			req.Header.Set("User-Agent", route.ua)
			resp, err := sharedHTTPClient().Do(req)
			if err != nil {
				result <- response{}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				result <- response{}
				return
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			if err != nil {
				result <- response{}
				return
			}
			result <- response{models: parseModelCatalog(data)}
		}()
	}
	merged := make(map[string]pluginapi.ModelInfo)
	for range routes {
		r := <-result
		for _, m := range r.models {
			if old, ok := merged[m.ID]; !ok || (old.ContextLength <= 131072 && m.ContextLength > old.ContextLength) {
				merged[m.ID] = m
			}
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]pluginapi.ModelInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, merged[k])
	}
	return out
}

// Parse JSON envelopes from both the CodeBuddy CLI enterprise catalog and
// the desktop /v3/config catalog. Ignore disabled and non-chat entries.
func parseModelCatalog(raw []byte) []pluginapi.ModelInfo {
	var env map[string]any
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	if code, ok := env["code"].(float64); ok && code != 0 {
		return nil
	}
	data := any(env)
	if x, ok := env["data"]; ok {
		data = x
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	entries, ok := obj["models"].([]any)
	if !ok {
		return nil
	}
	out := make([]pluginapi.ModelInfo, 0, len(entries))
	for _, rawEntry := range entries {
		row, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		if row["disabled"] == true || row["enabled"] == false {
			continue
		}
		id := ""
		for _, k := range []string{"id", "modelId", "model", "name"} {
			if s, ok := row[k].(string); ok && s != "" {
				id = s
				break
			}
		}
		if !isChatModelID(id) {
			continue
		}
		maxOutput := int64(8192)
		for _, k := range []string{"maxOutputTokens", "max_output_tokens"} {
			if n, ok := row[k].(float64); ok && n > 0 {
				maxOutput = int64(n)
				break
			}
		}
		if maxOutput <= 256 {
			continue
		}
		if tags, ok := row["tags"].([]any); ok {
			image := false
			for _, t := range tags {
				if fmt.Sprint(t) == "text-to-image" {
					image = true
				}
			}
			if image {
				continue
			}
		}
		ctx := int64(131072)
		for _, k := range []string{"maxInputTokens", "contextWindow", "context_length"} {
			if n, ok := row[k].(float64); ok && n > 0 {
				ctx = int64(n)
				break
			}
		}
		name := id
		if s, ok := row["name"].(string); ok && s != "" {
			name = s
		}
		out = append(out, pluginapi.ModelInfo{ID: id, Object: "model", OwnedBy: providerName, DisplayName: name, Name: id,
			SupportedGenerationMethods: []string{"chat"}, ContextLength: ctx, MaxCompletionTokens: maxOutput, UserDefined: true})
	}
	return out
}

func isChatModelID(id string) bool {
	if len(id) < 2 || len(id) > 90 {
		return false
	}
	id = strings.TrimSpace(id)
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	for _, prefix := range []string{"nes-", "completion-", "codewise-", "hunyuan-image-"} {
		if strings.HasPrefix(id, prefix) {
			return false
		}
	}
	for _, virtual := range []string{"auto", "auto-chat", "default", "default-1.1", "default-1.2", "default-model", "fast-model", "balanced-model", "primary-model", "deep-model"} {
		if id == virtual {
			return false
		}
	}
	return true
}
