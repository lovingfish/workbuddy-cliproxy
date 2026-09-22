package main

import (
	"fmt"
	"sort"
)

// normalizeToolChoice preserves a forced function by limiting the offered tools
// to that function and using the upstream's string-valued "required" choice.
func normalizeToolChoice(body map[string]any) error {
	value, present := body["tool_choice"]
	if !present || value == nil {
		return nil
	}
	if choice, ok := value.(string); ok {
		switch choice {
		case "auto", "none", "required":
			return nil
		}
		return &requestError{status: 400, message: "unsupported tool_choice value"}
	}
	choice, ok := value.(map[string]any)
	if !ok || choice["type"] != "function" {
		return &requestError{status: 400, message: "unsupported tool_choice object"}
	}
	function, _ := choice["function"].(map[string]any)
	name, _ := function["name"].(string)
	if name == "" {
		return &requestError{status: 400, message: "forced tool_choice requires a function name"}
	}
	entries, _ := body["tools"].([]any)
	for _, entry := range entries {
		tool, ok := entry.(map[string]any)
		if !ok || tool["type"] != "function" {
			continue
		}
		candidate, _ := tool["function"].(map[string]any)
		if candidate["name"] == name {
			body["tools"] = []any{entry}
			body["tool_choice"] = "required"
			return nil
		}
	}
	return &requestError{status: 400, message: fmt.Sprintf("forced function %q is missing from tools", name)}
}

func mergeToolCallDelta(calls map[int]map[string]any, delta map[string]any, fallback int) {
	index := fallback
	if value, ok := delta["index"].(float64); ok {
		index = int(value)
	}
	if index < 0 {
		return
	}
	target := calls[index]
	if target == nil {
		target = map[string]any{"type": "function", "function": map[string]any{}}
		calls[index] = target
	}
	for _, key := range []string{"id", "type"} {
		if value, ok := delta[key].(string); ok && value != "" {
			target[key] = value
		}
	}
	incoming, ok := delta["function"].(map[string]any)
	if !ok {
		return
	}
	function := target["function"].(map[string]any)
	if name, ok := incoming["name"].(string); ok && name != "" {
		current, _ := function["name"].(string)
		if name != current {
			function["name"] = current + name
		}
	}
	if arguments, ok := incoming["arguments"].(string); ok {
		current, _ := function["arguments"].(string)
		function["arguments"] = current + arguments
	}
}

func orderedToolCalls(calls map[int]map[string]any) []map[string]any {
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	result := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		result = append(result, calls[index])
	}
	return result
}
