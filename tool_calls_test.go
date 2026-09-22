//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestForcedToolChoiceIsTranslatedWithoutChangingTarget(t *testing.T) {
	body := map[string]any{
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "wanted"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "other"}},
			map[string]any{"type": "function", "function": map[string]any{"name": "wanted"}},
		},
	}
	if err := normalizeToolChoice(body); err != nil {
		t.Fatal(err)
	}
	if body["tool_choice"] != "required" || len(body["tools"].([]any)) != 1 {
		t.Fatal(body)
	}
	chosen := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"]
	if chosen != "wanted" {
		t.Fatal("forced function changed")
	}
	missing := map[string]any{"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "absent"}}}
	if err := normalizeToolChoice(missing); err == nil {
		t.Fatal("missing forced function accepted")
	}
}
func TestNonStreamAggregationMergesToolArguments(t *testing.T) {
	input := `data: {"id":"test","model":"glm-5.3-flash","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_test","type":"function","function":{"name":"deployment_probe","arguments":"{"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"status\":\"ok\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	raw, err := aggregateCompletion(strings.NewReader(input), "glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	calls := response.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_test" || calls[0].Function.Name != "deployment_probe" || calls[0].Function.Arguments != `{"status":"ok"}` {
		t.Fatalf("invalid merged calls: %s", raw)
	}
	if response.Choices[0].FinishReason != "tool_calls" {
		t.Fatal("finish reason lost")
	}
}
