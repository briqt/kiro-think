package inject

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/briqt/kiro-think/internal/config"
)

var tagRe = regexp.MustCompile(`<(?:thinking_mode|thinking|max_thinking_length|thinking_effort|budget|effort)>[^<]*</(?:thinking_mode|thinking|max_thinking_length|thinking_effort|budget|effort)>\s*`)

// GeneratePrefix returns the XML tag prefix for the given thinking config.
func GeneratePrefix(tc config.ThinkingConfig) string {
	if tc.Mode == "adaptive" {
		return fmt.Sprintf("<thinking_mode>adaptive</thinking_mode><thinking_effort>%s</thinking_effort>", tc.Level)
	}
	return fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", tc.Budget)
}

// hasThinkingTags checks if content already contains thinking tags.
func hasThinkingTags(content string) bool {
	return strings.Contains(content, "<thinking_mode>") || strings.Contains(content, "<max_thinking_length>")
}

// isModelAllowed checks if modelID is in the allowed list.
func isModelAllowed(modelID string, models []string) bool {
	for _, m := range models {
		if strings.EqualFold(m, modelID) {
			return true
		}
	}
	return false
}

// injectIntoContent prepends thinking prefix into a content string.
// Returns empty string if tags already present (skip injection).
func injectIntoContent(content string, tc config.ThinkingConfig) (string, bool) {
	if hasThinkingTags(content) {
		return content, false
	}
	cleaned := strings.TrimLeft(tagRe.ReplaceAllString(content, ""), "\n")
	return GeneratePrefix(tc) + "\n" + cleaned, true
}

// InjectResult describes the outcome of an injection attempt.
type InjectResult struct {
	Body    []byte
	Done    bool
	ModelID string
	Reason  string // empty if Done=true; "model_filtered", "already_tagged", "no_target" etc.
}

// InjectThinking modifies the request body to inject thinking tags.
// If models is empty, injection applies to all models (no whitelist filtering).
func InjectThinking(body []byte, tc config.ThinkingConfig, models []string) InjectResult {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return InjectResult{Body: body, Reason: "parse_error"}
	}

	modelID := extractModelID(req)

	// Check whitelist (empty list = allow all)
	if len(models) > 0 && !isModelAllowed(modelID, models) {
		return InjectResult{Body: body, ModelID: modelID, Reason: "model_filtered"}
	}

	csRaw, ok := req["conversationState"]
	if !ok {
		return InjectResult{Body: body, ModelID: modelID, Reason: "no_conversation_state"}
	}
	var cs map[string]json.RawMessage
	if err := json.Unmarshal(csRaw, &cs); err != nil {
		return InjectResult{Body: body, ModelID: modelID, Reason: "parse_error"}
	}

	// Try history first
	if result, injected, alreadyTagged := injectInHistory(cs, tc); injected {
		cs["history"] = result
		csBytes, _ := json.Marshal(cs)
		req["conversationState"] = csBytes
		out, err := json.Marshal(req)
		if err != nil {
			return InjectResult{Body: body, ModelID: modelID, Reason: "marshal_error"}
		}
		return InjectResult{Body: out, Done: true, ModelID: modelID}
	} else if alreadyTagged {
		return InjectResult{Body: body, ModelID: modelID, Reason: "already_tagged"}
	}

	// Fallback: inject into currentMessage when history is empty
	if result, injected := injectInCurrentMessage(cs, tc); injected {
		cs["currentMessage"] = result
		csBytes, _ := json.Marshal(cs)
		req["conversationState"] = csBytes
		out, err := json.Marshal(req)
		if err != nil {
			return InjectResult{Body: body, ModelID: modelID, Reason: "marshal_error"}
		}
		return InjectResult{Body: out, Done: true, ModelID: modelID}
	}

	// Neither history nor currentMessage could be injected
	return InjectResult{Body: body, ModelID: modelID, Reason: "no_injection_target"}
}

// injectInHistory injects into the first user message in history.
// Returns (result, true) on success, (nil, false) if history is empty/missing,
// or (body, false) with non-nil body if tags already present (should not fallback).
func injectInHistory(cs map[string]json.RawMessage, tc config.ThinkingConfig) (json.RawMessage, bool, bool) {
	histRaw, ok := cs["history"]
	if !ok {
		return nil, false, false
	}
	var history []json.RawMessage
	if err := json.Unmarshal(histRaw, &history); err != nil || len(history) == 0 {
		return nil, false, false
	}

	for i, msgRaw := range history {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(msgRaw, &msg); err != nil {
			continue
		}
		uimRaw, ok := msg["userInputMessage"]
		if !ok {
			continue
		}
		var uim map[string]json.RawMessage
		if err := json.Unmarshal(uimRaw, &uim); err != nil {
			return nil, false, true
		}
		contentRaw, ok := uim["content"]
		if !ok {
			return nil, false, true
		}
		var content string
		if err := json.Unmarshal(contentRaw, &content); err != nil {
			return nil, false, true
		}

		newContent, injected := injectIntoContent(content, tc)
		if !injected {
			// Tags already present — do not fallback
			return nil, false, true
		}

		contentBytes, _ := json.Marshal(newContent)
		uim["content"] = contentBytes
		uimBytes, _ := json.Marshal(uim)
		msg["userInputMessage"] = uimBytes
		msgBytes, _ := json.Marshal(msg)
		history[i] = msgBytes

		histBytes, _ := json.Marshal(history)
		return histBytes, true, false
	}
	return nil, false, false
}

// injectInCurrentMessage injects into currentMessage.userInputMessage.content.
func injectInCurrentMessage(cs map[string]json.RawMessage, tc config.ThinkingConfig) (json.RawMessage, bool) {
	cmRaw, ok := cs["currentMessage"]
	if !ok {
		return nil, false
	}
	var cm map[string]json.RawMessage
	if err := json.Unmarshal(cmRaw, &cm); err != nil {
		return nil, false
	}
	uimRaw, ok := cm["userInputMessage"]
	if !ok {
		return nil, false
	}
	var uim map[string]json.RawMessage
	if err := json.Unmarshal(uimRaw, &uim); err != nil {
		return nil, false
	}
	contentRaw, ok := uim["content"]
	if !ok {
		return nil, false
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return nil, false
	}

	newContent, injected := injectIntoContent(content, tc)
	if !injected {
		return nil, false
	}

	contentBytes, _ := json.Marshal(newContent)
	uim["content"] = contentBytes
	uimBytes, _ := json.Marshal(uim)
	cm["userInputMessage"] = uimBytes
	cmBytes, _ := json.Marshal(cm)
	return cmBytes, true
}

func extractModelID(req map[string]json.RawMessage) string {
	csRaw, ok := req["conversationState"]
	if !ok {
		return ""
	}
	var cs struct {
		CurrentMessage struct {
			UserInputMessage struct {
				ModelID string `json:"modelId"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
	}
	if err := json.Unmarshal(csRaw, &cs); err != nil {
		return ""
	}
	return cs.CurrentMessage.UserInputMessage.ModelID
}
