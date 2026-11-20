package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yugrinovD/gogate/internal/provider"
)

type messageIn struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []messageIn     `json:"messages"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	N                   *int            `json:"n"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	Stop                json.RawMessage `json:"stop"`
	Seed                *int            `json:"seed"`
	PresencePenalty     *float64        `json:"presence_penalty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty"`
	Stream              bool            `json:"stream"`
	StreamOptions       *streamOptions  `json:"stream_options"`
	User                string          `json:"user,omitempty"`
}

type usageDTO struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type messageDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type choiceDTO struct {
	Index        int        `json:"index"`
	Message      messageDTO `json:"message"`
	FinishReason string     `json:"finish_reason"`
}

type chatResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []choiceDTO `json:"choices"`
	Usage   usageDTO    `json:"usage"`
}

type deltaDTO struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type chunkChoiceDTO struct {
	Index        int      `json:"index"`
	Delta        deltaDTO `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type chatChunk struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []chunkChoiceDTO `json:"choices"`
	Usage   *usageDTO        `json:"usage,omitempty"`
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

func parseMessages(in []messageIn) ([]provider.Message, error) {
	out := make([]provider.Message, 0, len(in))
	for i, m := range in {
		role := strings.TrimSpace(m.Role)
		switch role {
		case "system", "user", "assistant", "tool", "developer":
		default:
			return nil, fmt.Errorf("messages[%d].role is invalid", i)
		}
		content, err := parseContent(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d].content: %w", i, err)
		}
		out = append(out, provider.Message{Role: role, Content: content})
	}
	return out, nil
}

func parseContent(raw json.RawMessage) (string, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, p := range parts {
			switch p.Type {
			case "text", "input_text":
				b.WriteString(p.Text)
			default:
				return "", fmt.Errorf("content type %q is not supported", p.Type)
			}
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("content must be a string or an array of text parts")
}

func parseStop(raw json.RawMessage) ([]string, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	}
	if raw[0] == '[' {
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		if len(list) > 4 {
			return nil, fmt.Errorf("at most 4 stop sequences are supported")
		}
		return list, nil
	}
	return nil, fmt.Errorf("stop must be a string or an array of strings")
}

func toUsageDTO(u provider.Usage) usageDTO {
	return usageDTO{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
}
