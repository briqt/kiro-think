package inject

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/briqt/kiro-think/internal/config"
)

var tagRe = regexp.MustCompile(`<(?:thinking|budget|effort)>[^<]*</(?:thinking|budget|effort)>\s*`)

// GeneratePrefix returns the XML tag prefix for the given thinking config.
func GeneratePrefix(tc config.ThinkingConfig) string {
	if tc.Mode == "adaptive" {
		return fmt.Sprintf("<thinking>adaptive</thinking><effort>%s</effort>", tc.Level)
	}
	return fmt.Sprintf("<thinking>enabled</thinking><budget>%d</budget>", tc.Budget)
}

// InjectThinking modifies the request body JSON to inject thinking tags
// into the first user message in conversation history.
// Returns modified body and whether injection occurred.
func InjectThinking(body []byte, tc config.ThinkingConfig) ([]byte, bool) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	csRaw, ok := req["conversationState"]
	if !ok {
		return body, false
	}

	var cs map[string]json.RawMessage
	if err := json.Unmarshal(csRaw, &cs); err != nil {
		return body, false
	}

	histRaw, ok := cs["history"]
	if !ok {
		return body, false
	}

	var history []json.RawMessage
	if err := json.Unmarshal(histRaw, &history); err != nil || len(history) == 0 {
		return body, false
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

		// Marshal back
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
			return body, false
		}
		return result, true
	}

	return body, false
}
