package inject

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/briqt/kiro-think/internal/config"
)

var defaultTC = config.ThinkingConfig{Mode: "enabled", Level: "max", Budget: 24576}
var adaptiveTC = config.ThinkingConfig{Mode: "adaptive", Level: "high", Budget: 20000}
var defaultModels = []string{"claude-sonnet-4.5", "claude-opus-4.6"}

func makeBody(history []map[string]any, currentContent, modelID string) []byte {
	body := map[string]any{
		"conversationState": map[string]any{
			"conversationId": "test-conv",
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": currentContent,
					"modelId": modelID,
				},
			},
			"history": history,
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// extractHistoryContent extracts the first user message content from history in the result body.
func extractHistoryContent(t *testing.T, body []byte) string {
	t.Helper()
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	var cs struct {
		History []struct {
			UserInputMessage *struct {
				Content string `json:"content"`
			} `json:"userInputMessage"`
		} `json:"history"`
	}
	json.Unmarshal(parsed["conversationState"], &cs)
	for _, h := range cs.History {
		if h.UserInputMessage != nil {
			return h.UserInputMessage.Content
		}
	}
	return ""
}

// extractCurrentContent extracts currentMessage.userInputMessage.content from the result body.
func extractCurrentContent(t *testing.T, body []byte) string {
	t.Helper()
	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)
	var cs struct {
		CurrentMessage struct {
			UserInputMessage struct {
				Content string `json:"content"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
	}
	json.Unmarshal(parsed["conversationState"], &cs)
	return cs.CurrentMessage.UserInputMessage.Content
}

func TestInjectThinking_HistoryPresent(t *testing.T) {
	history := []map[string]any{
		{"userInputMessage": map[string]any{"content": "system prompt", "modelId": "claude-opus-4.6"}},
		{"assistantResponseMessage": map[string]any{"content": "ok"}},
	}
	body := makeBody(history, "hello", "claude-opus-4.6")

	res := InjectThinking(body, defaultTC, defaultModels)
	if !res.Done {
		t.Fatalf("expected injection, got reason=%s", res.Reason)
	}
	content := extractHistoryContent(t, res.Body)
	if !strings.HasPrefix(content, "<thinking_mode>enabled</thinking_mode><max_thinking_length>24576</max_thinking_length>") {
		t.Fatalf("unexpected content: %s", content)
	}
	if !strings.Contains(content, "system prompt") {
		t.Fatal("original content should be preserved")
	}
}

func TestInjectThinking_HistoryEmpty_FallbackToCurrentMessage(t *testing.T) {
	body := makeBody([]map[string]any{}, "hello world", "claude-opus-4.6")

	res := InjectThinking(body, defaultTC, defaultModels)
	if !res.Done {
		t.Fatalf("expected injection into currentMessage, got reason=%s", res.Reason)
	}
	content := extractCurrentContent(t, res.Body)
	if !strings.HasPrefix(content, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("expected prefix in currentMessage, got: %s", content)
	}
}

func TestInjectThinking_AlreadyTagged(t *testing.T) {
	history := []map[string]any{
		{"userInputMessage": map[string]any{
			"content": "<thinking_mode>enabled</thinking_mode><max_thinking_length>10000</max_thinking_length>\nsystem prompt",
			"modelId": "claude-opus-4.6",
		}},
	}
	body := makeBody(history, "hello", "claude-opus-4.6")

	res := InjectThinking(body, defaultTC, defaultModels)
	if res.Done {
		t.Fatal("expected skip when tags already present")
	}
}

func TestInjectThinking_ModelFiltered(t *testing.T) {
	body := makeBody(nil, "hello", "claude-haiku-4.5")

	res := InjectThinking(body, defaultTC, defaultModels)
	if res.Done {
		t.Fatal("expected skip for filtered model")
	}
	if res.Reason != "model_filtered" {
		t.Fatalf("expected reason=model_filtered, got %s", res.Reason)
	}
}

func TestInjectThinking_EmptyModels_AllowAll(t *testing.T) {
	body := makeBody([]map[string]any{}, "hello", "claude-haiku-4.5")

	res := InjectThinking(body, defaultTC, []string{})
	if !res.Done {
		t.Fatalf("expected injection with empty models list, got reason=%s", res.Reason)
	}
}

func TestInjectThinking_AdaptiveMode(t *testing.T) {
	history := []map[string]any{
		{"userInputMessage": map[string]any{"content": "prompt", "modelId": "claude-sonnet-4.5"}},
	}
	body := makeBody(history, "hello", "claude-sonnet-4.5")

	res := InjectThinking(body, adaptiveTC, defaultModels)
	if !res.Done {
		t.Fatalf("expected injection, got reason=%s", res.Reason)
	}
	content := extractHistoryContent(t, res.Body)
	if !strings.Contains(content, "<thinking_mode>adaptive</thinking_mode>") {
		t.Fatalf("expected adaptive tag, got: %s", content)
	}
	if !strings.Contains(content, "<thinking_effort>high</thinking_effort>") {
		t.Fatalf("expected effort tag, got: %s", content)
	}
}

func TestInjectThinking_StripsOldTags(t *testing.T) {
	history := []map[string]any{
		{"userInputMessage": map[string]any{
			"content": "<thinking>enabled</thinking><budget>10000</budget>\nold system prompt",
			"modelId": "claude-opus-4.6",
		}},
	}
	body := makeBody(history, "hello", "claude-opus-4.6")

	res := InjectThinking(body, defaultTC, defaultModels)
	if !res.Done {
		t.Fatalf("expected injection, got reason=%s", res.Reason)
	}
	content := extractHistoryContent(t, res.Body)
	if strings.Contains(content, "<thinking>enabled</thinking>") {
		t.Fatal("old tags should be stripped")
	}
	if strings.Contains(content, "<budget>") {
		t.Fatal("old budget tag should be stripped")
	}
	if !strings.Contains(content, "old system prompt") {
		t.Fatal("original content should be preserved")
	}
}

func TestGeneratePrefix(t *testing.T) {
	enabled := GeneratePrefix(config.ThinkingConfig{Mode: "enabled", Budget: 24576})
	if enabled != "<thinking_mode>enabled</thinking_mode><max_thinking_length>24576</max_thinking_length>" {
		t.Fatalf("unexpected enabled prefix: %s", enabled)
	}

	adaptive := GeneratePrefix(config.ThinkingConfig{Mode: "adaptive", Level: "high"})
	if adaptive != "<thinking_mode>adaptive</thinking_mode><thinking_effort>high</thinking_effort>" {
		t.Fatalf("unexpected adaptive prefix: %s", adaptive)
	}
}
