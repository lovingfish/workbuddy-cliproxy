package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lovingfish/workbuddy-cliproxy/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type catalogConfiguration struct {
	Catalog struct {
		Channel         string `yaml:"channel"`
		TTLSeconds      int    `yaml:"ttl-seconds"`
		MaxStaleSeconds int    `yaml:"max-stale-seconds"`
	} `yaml:"catalog"`
}
type catalogRuntimeState struct {
	mu        sync.RWMutex
	manager   *catalog.Manager
	snapshots map[string]catalog.Snapshot
	options   catalog.Options
}

var dynamicCatalog = &catalogRuntimeState{snapshots: map[string]catalog.Snapshot{}}

func configureCatalog(raw []byte) error {
	var request struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return fmt.Errorf("decode plugin lifecycle request: %w", err)
		}
	}
	var settings catalogConfiguration
	if len(request.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(request.ConfigYAML, &settings); err != nil {
			return fmt.Errorf("decode catalog settings: %w", err)
		}
	}
	options := catalog.Options{Channel: settings.Catalog.Channel,
		TTL:      time.Duration(settings.Catalog.TTLSeconds) * time.Second,
		MaxStale: time.Duration(settings.Catalog.MaxStaleSeconds) * time.Second}
	if options.Channel == "" {
		options.Channel = "cli"
	}
	if options.TTL <= 0 {
		options.TTL = 5 * time.Minute
	}
	if options.MaxStale <= 0 {
		options.MaxStale = 24 * time.Hour
	}
	dynamicCatalog.mu.Lock()
	defer dynamicCatalog.mu.Unlock()
	if dynamicCatalog.manager == nil {
		client := &http.Client{Timeout: 20 * time.Second, Transport: sharedHTTPClient().Transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		dynamicCatalog.manager = catalog.NewManager(catalog.HTTPFetcher(client, catalog.ConfigURL, clientUA), options)
	} else {
		dynamicCatalog.manager.Reconfigure(options)
	}
	if dynamicCatalog.options.Channel != "" && dynamicCatalog.options.Channel != options.Channel {
		dynamicCatalog.snapshots = map[string]catalog.Snapshot{}
	}
	dynamicCatalog.options = options
	return nil
}
func catalogCredentials(sa *storedAuth) catalog.Credentials {
	return catalog.Credentials{AccessToken: sa.Auth.AccessToken, UID: sa.Account.UID,
		EnterpriseID: sa.Account.EnterpriseID, Domain: sa.Auth.Domain}
}
func accountCatalog(authID string, sa *storedAuth) (catalog.Snapshot, error) {
	if authID == "" {
		authID = providerName
	}
	dynamicCatalog.mu.RLock()
	manager := dynamicCatalog.manager
	dynamicCatalog.mu.RUnlock()
	if manager == nil {
		return catalog.Snapshot{}, fmt.Errorf("catalog is not initialized")
	}
	snapshot, err := manager.Get(context.Background(), catalogCredentials(sa))
	if err != nil {
		dynamicCatalog.mu.Lock()
		delete(dynamicCatalog.snapshots, authID)
		dynamicCatalog.mu.Unlock()
		return catalog.Snapshot{}, err
	}
	dynamicCatalog.mu.Lock()
	dynamicCatalog.snapshots[authID] = snapshot
	dynamicCatalog.mu.Unlock()
	return snapshot, nil
}
func handleCatalogForAuth(raw []byte) ([]byte, error) {
	var request pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	sa, err := parseStored(request.StorageJSON)
	if err != nil {
		return nil, err
	}
	snapshot, err := accountCatalog(request.AuthID, sa)
	if err != nil {
		return nil, fmt.Errorf("discover WorkBuddy catalog: %w", err)
	}
	models := make([]pluginapi.ModelInfo, 0, len(snapshot.Models))
	for _, item := range snapshot.Models {
		info := pluginapi.ModelInfo{ID: item.ID, Object: "model", OwnedBy: providerName, DisplayName: item.Name,
			Name: item.ID, Description: item.Description, UserDefined: true,
			SupportedGenerationMethods: []string{"chat"}}
		if value, ok := item.Public["contextLength"].(float64); ok && value > 0 {
			info.ContextLength = int64(value)
		}
		if value, ok := item.Public["maxCompletionTokens"].(float64); ok && value > 0 {
			info.MaxCompletionTokens = int64(value)
		}
		if levels := item.SupportedEfforts(); len(levels) > 0 {
			allowed, _ := item.Reasoning["canDisableThinking"].(bool)
			info.Thinking = &pluginapi.ThinkingSupport{Levels: levels, ZeroAllowed: allowed}
		}
		if item.Public["supportsReasoning"] == true || len(item.Reasoning) > 0 {
			info.SupportedParameters = []string{"reasoning_effort"}
		}
		models = append(models, info)
	}
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

type requestError struct {
	status  int
	message string
}

func (e *requestError) Error() string   { return e.message }
func (e *requestError) StatusCode() int { return e.status }

func prepareUpstreamPayload(req pluginapi.ExecutorRequest, sa *storedAuth) ([]byte, error) {
	snapshot, err := accountCatalog(req.AuthID, sa)
	if err != nil {
		return nil, &requestError{status: 503, message: "WorkBuddy catalog discovery failed; retry or check the upstream credential"}
	}
	model, ok := snapshot.Find(req.Model)
	if !ok {
		return nil, &requestError{status: 400, message: fmt.Sprintf("model %q is not listed for the selected account and channel %q", req.Model, snapshot.Channel)}
	}
	source := req.Payload
	if len(source) == 0 {
		source = req.OriginalRequest
	}
	var body map[string]any
	if err = json.Unmarshal(source, &body); err != nil || body == nil {
		return nil, &requestError{status: 400, message: "invalid chat completion request body"}
	}
	// The host has already resolved aliases; preserve this native model ID.
	body["model"] = req.Model
	body["stream"] = true
	if err = normalizeToolChoice(body); err != nil {
		return nil, err
	}
	if err = model.ApplyReasoning(body); err != nil {
		return nil, &requestError{status: 400, message: err.Error()}
	}
	result, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return rewriteSystemForUpstream(result), nil
}

type catalogInterceptResponse struct {
	Headers      http.Header
	Body         []byte
	ClearHeaders []string
}

func handleCatalogResponse(raw []byte) ([]byte, error) {
	var request pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	path, _ := request.Metadata["request_path"].(string)
	unchanged := catalogInterceptResponse{Headers: request.ResponseHeaders, Body: request.Body}
	if (path != "" && path != "/v1/models") || request.Stream || request.StatusCode != http.StatusOK {
		return okEnvelope(unchanged)
	}
	// CPA's model-list writer intentionally passes nil Metadata. Distinguish
	// that context from inference responses without relying on a missing path.
	if path == "" && (request.Model != "" || request.RequestedModel != "" || len(request.RequestBody) > 0 || len(request.OriginalRequest) > 0) {
		return okEnvelope(unchanged)
	}
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		return okEnvelope(unchanged)
	}
	if path == "" && body["object"] != "list" {
		return okEnvelope(unchanged)
	}
	rows, ok := body["data"].([]any)
	if !ok {
		return okEnvelope(unchanged)
	}
	dynamicCatalog.mu.RLock()
	snapshots := make([]catalog.Snapshot, 0, len(dynamicCatalog.snapshots))
	for _, snapshot := range dynamicCatalog.snapshots {
		snapshots = append(snapshots, snapshot)
	}
	dynamicCatalog.mu.RUnlock()
	changed := false
	for _, entry := range rows {
		row, ok := entry.(map[string]any)
		if !ok || row["owned_by"] != providerName || (row["object"] != "model" && row["type"] != "model") {
			continue
		}
		id, _ := row["id"].(string)
		var variants []map[string]any
		for _, snapshot := range snapshots {
			if model, found := snapshot.Find(id); found {
				variants = append(variants, snapshot.PublicFields(model, time.Now()))
			}
		}
		if len(variants) == 1 {
			for key, value := range variants[0] {
				row[key] = value
			}
			changed = true
		} else if len(variants) > 1 {
			// Do not turn differing account prices or effort sets into one false value.
			row["catalog_variants"] = variants
			changed = true
		} else {
			// The host can retain registration after a discovery failure. Do not
			// present a previous account's prices or capabilities as current.
			row["catalog"] = map[string]any{"provider": providerName, "stale": true, "availability": "metadata_unavailable"}
			changed = true
		}
	}
	if !changed {
		return okEnvelope(unchanged)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return okEnvelope(catalogInterceptResponse{Headers: request.ResponseHeaders, Body: encoded, ClearHeaders: []string{"Content-Length", "ETag"}})
}
