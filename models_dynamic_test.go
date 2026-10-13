package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	payload := `{"code":0,"data":{"models":[
 {"id":"glm-5.3","name":"GLM 5.3","maxInputTokens":262144,"maxOutputTokens":8192},
 {"modelId":"hy4-preview-f","maxOutputTokens":4096},
 {"id":"blocked-model","disabled":true},
 {"id":"completion-1.0","maxOutputTokens":128},
 {"id":"hunyuan-image-v3.0","tags":["text-to-image"]}
 ]}}`
	got := parseModelCatalog([]byte(payload))
	if len(got) != 2 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	if got[0].ID != "glm-5.3" || got[0].ContextLength != 262144 {
		t.Errorf("unexpected glm: %+v", got[0])
	}
	if got[1].ID != "hy4-preview-f" {
		t.Errorf("unexpected HY: %+v", got[1])
	}
	if len(parseModelCatalog([]byte(`{"code":401,"data":{"models":[{"id":"glm-5.3"}]}}`))) != 0 {
		t.Fatal("API errors must not produce models")
	}
}
func TestAggregateToolCallDeltas(t *testing.T) {
	lines := []any{
		map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call1", "type": "function", "function": map[string]any{"name": "add", "arguments": "{"}}}}}}},
		map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "\"a\":2,"}}}}}}},
		map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "\"b\":3}"}}}}, "finish_reason": "tool_calls"}}},
	}
	var src strings.Builder
	for _, line := range lines {
		j, _ := json.Marshal(line)
		src.WriteString("data: " + string(j) + "\n\n")
	}
	result, err := aggregateCompletion(strings.NewReader(src.String()), "glm-5.3")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(result, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Choices) != 1 || len(parsed.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 complete tool call: %s", result)
	}
	c := parsed.Choices[0].Message.ToolCalls[0]
	if c.ID != "call1" || c.Function.Name != "add" || c.Function.Arguments != `{"a":2,"b":3}` {
		t.Fatalf("malformed tool call: %+v", c)
	}
}
