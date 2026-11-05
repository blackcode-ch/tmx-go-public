package claude

import "encoding/json"

// Lines written to claude's stdin (one JSON object each, newline-terminated).

func mustLine(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only maps, strings and RawMessages that came from valid JSON
	}
	return append(b, '\n')
}

func controlRequest(id, subtype string) []byte {
	return mustLine(map[string]any{
		"type": "control_request", "request_id": id,
		"request": map[string]any{"subtype": subtype},
	})
}

func userMessage(text, sessionID string) []byte {
	return mustLine(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         sessionID,
	})
}

func controlSuccess(id string, resp any) []byte {
	return mustLine(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": id, "response": resp,
		},
	})
}

func controlError(id, msg string) []byte {
	return mustLine(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "error", "request_id": id, "error": msg,
		},
	})
}

func permissionAllow(id string, input json.RawMessage) []byte {
	return controlSuccess(id, map[string]any{"behavior": "allow", "updatedInput": input})
}

func permissionDeny(id, msg string) []byte {
	if msg == "" {
		msg = "The owner denied this."
	}
	return controlSuccess(id, map[string]any{"behavior": "deny", "message": msg})
}
