//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lovingfish/workbuddy-cliproxy/internal/catalog"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const runtimeFixture = `{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3","credits":"x0.00 credits","reasoning":{"defaultEffort":"high","supportedEfforts":["low","high"]},"supportsReasoning":true}],"agents":[{"name":"cli","models":["hy3"]}]}}`

func setupRuntimeCatalog(t *testing.T) {
	t.Helper()
	previous := dynamicCatalog
	dynamicCatalog = &catalogRuntimeState{
		manager:   catalog.NewManager(func(context.Context, catalog.Credentials) ([]byte, error) { return []byte(runtimeFixture), nil }, catalog.Options{}),
		snapshots: map[string]catalog.Snapshot{},
	}
	t.Cleanup(func() { dynamicCatalog = previous })
}
func testAuth() *storedAuth {
	return &storedAuth{Auth: storedTokens{AccessToken: "TEST_TOKEN"}, Account: storedAccount{UID: "TEST_UID"}}
}
func TestPreparePayloadPreservesLowAndNativeModel(t *testing.T) {
	setupRuntimeCatalog(t)
	req := pluginapi.ExecutorRequest{AuthID: "test", Model: "hy3", Payload: []byte(`{"model":"hy3","messages":[{"role":"user","content":"ok"}],"reasoning_effort":"low","stream":false}`)}
	raw, err := prepareUpstreamPayload(req, testAuth())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if body["model"] != "hy3" || body["reasoning_effort"] != "low" || body["stream"] != true {
		t.Fatalf("wrong payload: %s", raw)
	}
	req.Model = "invented-model"
	if _, err := prepareUpstreamPayload(req, testAuth()); err == nil {
		t.Fatal("unlisted model was routed")
	}
}
func TestPrepareRejectsUnsupportedEffort(t *testing.T) {
	setupRuntimeCatalog(t)
	req := pluginapi.ExecutorRequest{AuthID: "test", Model: "hy3", Payload: []byte(`{"reasoning_effort":"max"}`)}
	_, err := prepareUpstreamPayload(req, testAuth())
	requestErr, ok := err.(*requestError)
	if !ok || requestErr.StatusCode() != 400 {
		t.Fatalf("error=%v", err)
	}
}
func TestModelMetadataInterceptorOnlyEnrichesOwnedRows(t *testing.T) {
	setupRuntimeCatalog(t)
	snapshot, err := accountCatalog("test", testAuth())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 1 {
		t.Fatal("unexpected catalog")
	}
	req := pluginapi.ResponseInterceptRequest{
		StatusCode: 200, Metadata: map[string]any{"request_path": "/v1/models"},
		Body: []byte(`{"object":"list","data":[{"id":"hy3","object":"model","owned_by":"workbuddy"},{"id":"hy3","object":"model","owned_by":"other"}]}`),
	}
	raw, _ := json.Marshal(req)
	response, err := handleCatalogResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(response, &env)
	var result catalogInterceptResponse
	_ = json.Unmarshal(env.Result, &result)
	var body struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(result.Body, &body)
	if body.Data[0]["display_name"] != "Hy3" || body.Data[0]["credits"] != "x0.00 credits" || body.Data[0]["reasoning"] == nil {
		t.Fatalf("missing metadata: %s", result.Body)
	}
	if _, ok := body.Data[1]["credits"]; ok {
		t.Fatal("other provider changed")
	}
	if strings.Contains(string(result.Body), "TEST_TOKEN") || strings.Contains(string(result.Body), "TEST_UID") {
		t.Fatal("credential leaked")
	}
	req.Metadata["request_path"] = "/v1/chat/completions"
	raw, _ = json.Marshal(req)
	response, _ = handleCatalogResponse(raw)
	_ = json.Unmarshal(response, &env)
	_ = json.Unmarshal(env.Result, &result)
	if string(result.Body) != string(req.Body) {
		t.Fatal("chat response changed")
	}
}
func TestStockCPAModelListWithoutRequestMetadata(t *testing.T) {
	setupRuntimeCatalog(t)
	_, _ = accountCatalog("test", testAuth())
	request := pluginapi.ResponseInterceptRequest{
		SourceFormat: "openai", StatusCode: 200,
		Body: []byte(`{"object":"list","data":[{"id":"hy3","object":"model","owned_by":"workbuddy"}]}`),
	}
	raw, _ := json.Marshal(request)
	result, err := handleCatalogResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(result, &env)
	var response catalogInterceptResponse
	_ = json.Unmarshal(env.Result, &response)
	if !strings.Contains(string(response.Body), "pricing") {
		t.Fatalf("stock CPA model response was not enriched: %s", response.Body)
	}
	request.Model = "hy3"
	raw, _ = json.Marshal(request)
	result, _ = handleCatalogResponse(raw)
	_ = json.Unmarshal(result, &env)
	_ = json.Unmarshal(env.Result, &response)
	if string(response.Body) != string(request.Body) {
		t.Fatal("inference response was enriched")
	}
}

func TestDiscoveryFailureInvalidatesPublicMetadata(t *testing.T) {
	setupRuntimeCatalog(t)
	_, _ = accountCatalog("test", testAuth())
	denied := catalog.NewManager(func(context.Context, catalog.Credentials) ([]byte, error) {
		return nil, &catalog.HTTPError{Status: 401}
	}, catalog.Options{})
	dynamicCatalog.manager = denied
	if _, err := accountCatalog("test", testAuth()); err == nil {
		t.Fatal("unauthorized discovery succeeded")
	}
	if len(dynamicCatalog.snapshots) != 0 {
		t.Fatal("previous account metadata was retained")
	}
	request := pluginapi.ResponseInterceptRequest{StatusCode: 200, Body: []byte(`{"object":"list","data":[{"id":"hy3","object":"model","owned_by":"workbuddy"}]}`)}
	raw, _ := json.Marshal(request)
	result, err := handleCatalogResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(result, &env)
	var response catalogInterceptResponse
	_ = json.Unmarshal(env.Result, &response)
	if !strings.Contains(string(response.Body), "metadata_unavailable") || strings.Contains(string(response.Body), "pricing") {
		t.Fatalf("unsafe retained metadata: %s", response.Body)
	}
}

func TestMissingAuthDoesNotAdvertiseStaticModels(t *testing.T) {
	response, err := handleMethod("model.static", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(response, &env)
	var models pluginapi.ModelResponse
	_ = json.Unmarshal(env.Result, &models)
	if len(models.Models) != 0 {
		t.Fatal("unauthenticated static models were advertised")
	}
}
func TestModelForAuthExposesReasoningSupport(t *testing.T) {
	setupRuntimeCatalog(t)
	authRaw, _ := json.Marshal(testAuth())
	request, _ := json.Marshal(pluginapi.AuthModelRequest{AuthID: "test", StorageJSON: authRaw})
	response, err := handleCatalogForAuth(request)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(response, &env)
	var models pluginapi.ModelResponse
	_ = json.Unmarshal(env.Result, &models)
	if len(models.Models) != 1 || models.Models[0].Thinking == nil || len(models.Models[0].Thinking.Levels) != 2 {
		t.Fatalf("models=%+v", models)
	}
}
func TestCatalogLifecycleRefreshKeepsLastGoodSnapshot(t *testing.T) {
	setupRuntimeCatalog(t)
	_, _ = accountCatalog("test", testAuth())
	req, _ := json.Marshal(map[string]any{"config_yaml": []byte("catalog:\n  channel: cli\n  ttl-seconds: 300\n_catalog_refresh: 1\n")})
	if err := configureCatalog(req); err != nil {
		t.Fatal(err)
	}
	if len(dynamicCatalog.snapshots) != 1 {
		t.Fatal("metadata disappeared during refresh")
	}
	for _, snapshot := range dynamicCatalog.snapshots {
		if time.Since(snapshot.FetchedAt) > time.Minute {
			t.Fatal("unexpected snapshot timestamp")
		}
	}
}
