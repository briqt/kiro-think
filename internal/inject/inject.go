package inject

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/briqt/kiro-think/internal/config"
)

var tagRe = regexp.MustCompile(`<(?:thinking|budget|effort)>[^<]*</(?:thinking|budget|effort)>\s*`)

// ThinkingModels is the whitelist of Kiro model IDs that support thinking.
// Haiku does not support extended thinking.
var ThinkingModels = map[string]bool{
	"claude-sonnet-4.5": true,
	"claude-sonnet-4.6": true,
	"claude-opus-4.5":   true,
	"claude-opus-4.6":   true,
}

// GeneratePrefix returns the XML tag prefix for the given thinking config.
func GeneratePrefix(tc config.ThinkingConfig) string {
	if tc.Mode == "adaptive" {
		return fmt.Sprintf("<thinking>adaptive</thinking><effort>%s</effort>", tc.Level)
	}
	return fmt.Sprintf("<thinking>enabled</thinking><budget>%d</budget>", tc.Budget)
}

// InjectThinking modifies the request body to inject thinking tags.
// Returns modified body, whether injection occurred, and the detected model ID.
func InjectThinking(body []byte, tc config.ThinkingConfig) ([]byte, bool, string) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false, ""
	}

	// Extract model ID from currentMessage.userInputMessage.modelId
	modelID := extractModelID(req)

	// Check whitelist
	if !ThinkingModels[modelID] {
		return body, false, modelID
	}

	csRaw, ok := req["conversationState"]
	if !ok {
		return body, false, modelID
	}
	var cs map[string]json.RawMessage
	if err := json.Unmarshal(csRaw, &cs); err != nil {
		return body, false, modelID
	}

	histRaw, ok := cs["history"]
	if !ok {
		return body, false, modelID
	}
	var history []json.RawMessage
	if err := json.Unmarshal(histRaw, &history); err != nil || len(history) == 0 {
		return body, false, modelID
	}

	// Find first user message
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
			break
		}
		contentRaw, ok := uim["content"]
		if !ok {
			break
		}
		var content string
		if err := json.Unmarshal(contentRaw, &content); err != nil {
			break
		}

		// Remove existing tags and inject new prefix
		cleaned := strings.TrimLeft(tagRe.ReplaceAllString(content, ""), "\n")
		prefix := GeneratePrefix(tc)
		content = prefix + "\n" + cleaned

		contentBytes, _ := json.Marshal(content)
		uim["content"] = contentBytes
		uimBytes, _ := json.Marshal(uim)
		msg["userInputMessage"] = uimBytes
		msgBytes, _ := json.Marshal(msg)
		history[i] = msgBytes

		histBytes, _ := json.Marshal(history)
		cs["history"] = histBytes
		csBytes, _ := json.Marshal(cs)
		req["conversationState"] = csBytes

		result, err := json.Marshal(req)
		if err != nil {
			return body, false, modelID
		}
		return result, true, modelID
	}

	return body, false, modelID
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
