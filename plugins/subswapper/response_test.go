package subswapperplugin_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Model explanations around a single fenced result are presentation, not a
// different routing decision. Multiple result blocks remain ambiguous.
func decodeSkillResponse(data []byte, result any) error {
	response := strings.TrimSpace(string(data))
	if !strings.Contains(response, "```") {
		return json.Unmarshal([]byte(response), result)
	}
	parts := strings.Split(response, "```")
	if len(parts) != 3 {
		return errors.New("expected a single JSON response block")
	}
	block := strings.TrimSpace(parts[1])
	block = strings.TrimPrefix(block, "json\n")
	block = strings.TrimPrefix(block, "json\r\n")
	return json.Unmarshal([]byte(block), result)
}

func TestDecodeSkillResponse(t *testing.T) {
	for _, response := range []string{
		`[{"id":"same-provider","route":"skill"}]`,
		"```json\n[{\"id\":\"same-provider\",\"route\":\"skill\"}]\n```",
		"Evaluating each scenario.\n\n```json\n[{\"id\":\"same-provider\",\"route\":\"skill\"}]\n```\n",
	} {
		var result []struct{ ID, Route string }
		if err := decodeSkillResponse([]byte(response), &result); err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 || result[0].ID != "same-provider" || result[0].Route != "skill" {
			t.Fatalf("wrong decision: %+v", result)
		}
	}
	for _, response := range []string{
		"no provider response",
		"```json\n[]",
		"```json\n[]\n```\n```json\n[]\n```",
		"```json\nnot json\n```",
	} {
		var result []struct{ ID, Route string }
		if err := decodeSkillResponse([]byte(response), &result); err == nil {
			t.Fatalf("accepted invalid or ambiguous response: %q", response)
		}
	}
}
