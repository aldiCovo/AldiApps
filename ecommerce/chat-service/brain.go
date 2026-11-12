package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

const (
	claudeModel = "claude-haiku-5-5"
	// Free on Gemini's free tier, fast, and calls tools.
	defaultGeminiModel = "gemini-3.5-flash-lite"

	aiGemini = "gemini"
	aiClaude = "claude"
	aiNone   = "none"
)

// errQuota is the AI refusing because the day's or minute's free requests are used up.
var errQuota = errors.New("the AI's quota is used up")

// chatTurn is one side's messages in a row: the customer's, or the shop's (bot and admins).
type chatTurn struct {
	customer bool
	texts    []string
}

// toolRun does one tool call and returns what the AI reads back, and whether it failed.
type toolRun func(ctx context.Context, name string, input json.RawMessage) (string, bool)

// brain is an AI that writes the bot's answers.
type brain interface {
	// think answers the conversation, which ends with the customer, calling run for each tool
	// it uses. rules stay the same between customers; moment changes with each answer.
	think(ctx context.Context, rules, moment string, history []chatTurn, run toolRun) (string, error)
	// private says whether the customer's name may be sent: not where the provider may keep
	// the conversation to train on.
	private() bool
}

// claudeBrain is Claude, paid from the shop's API credit.
type claudeBrain struct {
	client anthropic.Client
}

func newClaudeBrain(key, baseURL string) *claudeBrain {
	options := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(2), option.WithRequestTimeout(30 * time.Second)}
	if baseURL != "" {
		options = append(options, option.WithBaseURL(baseURL))
	}
	return &claudeBrain{client: anthropic.NewClient(options...)}
}

func (c *claudeBrain) private() bool { return true }

func (c *claudeBrain) think(ctx context.Context, rules, moment string, history []chatTurn, run toolRun) (string, error) {
	messages := make([]anthropic.MessageParam, len(history))
	for i, t := range history {
		blocks := make([]anthropic.ContentBlockParamUnion, len(t.texts))
		for j, text := range t.texts {
			blocks[j] = anthropic.NewTextBlock(text)
		}
		if t.customer {
			messages[i] = anthropic.NewUserMessage(blocks...)
		} else {
			messages[i] = anthropic.NewAssistantMessage(blocks...)
		}
	}
	tools := make([]anthropic.ToolUnionParam, len(botTools))
	for i, t := range botTools {
		tools[i] = anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: anthropic.ToolInputSchemaParam{Properties: t.properties, Required: t.required},
		}}
	}
	for round := 0; round < maxToolRounds; round++ {
		resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     claudeModel,
			MaxTokens: 1024,
			System: []anthropic.TextBlockParam{
				{Text: rules, CacheControl: anthropic.NewCacheControlEphemeralParam()},
				{Text: moment},
			},
			Messages:     messages,
			Tools:        tools,
			OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow},
		})
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			return "", errQuota
		}
		if err != nil {
			return "", err
		}
		switch resp.StopReason {
		case anthropic.StopReasonRefusal, anthropic.StopReasonMaxTokens:
			return "", fmt.Errorf("claude stopped: %s", resp.StopReason)
		}
		var text strings.Builder
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.TextBlock:
				text.WriteString(b.Text)
			case anthropic.ToolUseBlock:
				out, failed := run(ctx, b.Name, []byte(b.JSON.Input.Raw()))
				results = append(results, anthropic.NewToolResultBlock(b.ID, out, failed))
			}
		}
		if resp.StopReason != anthropic.StopReasonToolUse {
			return text.String(), nil
		}
		messages = append(messages, resp.ToParam(), anthropic.NewUserMessage(results...))
	}
	return "", fmt.Errorf("no answer after %d tool rounds", maxToolRounds)
}

// geminiBrain is Google's Gemini through its REST generateContent method. On the free tier
// Google may use what is sent to improve its products, so no names go to it.
type geminiBrain struct {
	baseURL string
	key     string
	model   string
	client  *http.Client
}

func newGeminiBrain(key, model, baseURL string) *geminiBrain {
	if model == "" {
		model = defaultGeminiModel
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	return &geminiBrain{baseURL: baseURL, key: key, model: model, client: &http.Client{Timeout: 30 * time.Second}}
}

func (g *geminiBrain) private() bool { return false }

type geminiText struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}

// geminiPart is what the bot reads of a part of Gemini's answer. The part itself goes back
// unchanged in the next request, with any thought signature it carries.
type geminiPart struct {
	Text         string `json:"text"`
	Thought      bool   `json:"thought"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

func (g *geminiBrain) think(ctx context.Context, rules, moment string, history []chatTurn, run toolRun) (string, error) {
	contents := make([]geminiContent, len(history))
	for i, t := range history {
		role := "model"
		if t.customer {
			role = "user"
		}
		parts := make([]any, len(t.texts))
		for j, text := range t.texts {
			parts[j] = geminiText{text}
		}
		contents[i] = geminiContent{Role: role, Parts: parts}
	}
	declarations := make([]map[string]any, len(botTools))
	for i, t := range botTools {
		declaration := map[string]any{"name": t.name, "description": t.description}
		if len(t.properties) > 0 {
			parameters := map[string]any{"type": "object", "properties": t.properties}
			if len(t.required) > 0 {
				parameters["required"] = t.required
			}
			declaration["parameters"] = parameters
		}
		declarations[i] = declaration
	}
	for round := 0; round < maxToolRounds; round++ {
		request := map[string]any{
			"systemInstruction": map[string]any{"parts": []geminiText{{rules}, {moment}}},
			"contents":          contents,
			"tools":             []map[string]any{{"functionDeclarations": declarations}},
			"generationConfig":  map[string]any{"maxOutputTokens": 1024},
		}
		var answer struct {
			Candidates []struct {
				Content struct {
					Parts []json.RawMessage `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if err := g.post(ctx, request, &answer); err != nil {
			return "", err
		}
		if len(answer.Candidates) == 0 {
			return "", errors.New("gemini gave no answer")
		}
		candidate := answer.Candidates[0]
		var text strings.Builder
		var results []any
		for _, raw := range candidate.Content.Parts {
			var part geminiPart
			if err := json.Unmarshal(raw, &part); err != nil {
				return "", fmt.Errorf("reading gemini's answer: %w", err)
			}
			switch {
			case part.FunctionCall != nil:
				out, failed := run(ctx, part.FunctionCall.Name, part.FunctionCall.Args)
				response := map[string]any{"name": part.FunctionCall.Name, "response": geminiResult(out, failed)}
				if part.FunctionCall.ID != "" {
					response["id"] = part.FunctionCall.ID
				}
				results = append(results, map[string]any{"functionResponse": response})
			case !part.Thought:
				text.WriteString(part.Text)
			}
		}
		if len(results) == 0 {
			if candidate.FinishReason != "" && candidate.FinishReason != "STOP" {
				return "", fmt.Errorf("gemini stopped: %s", candidate.FinishReason)
			}
			return text.String(), nil
		}
		modelParts := make([]any, len(candidate.Content.Parts))
		for i, raw := range candidate.Content.Parts {
			modelParts[i] = raw
		}
		contents = append(contents, geminiContent{Role: "model", Parts: modelParts}, geminiContent{Role: "user", Parts: results})
	}
	return "", fmt.Errorf("no answer after %d tool rounds", maxToolRounds)
}

// geminiResult wraps a tool's output as the object Gemini wants back.
func geminiResult(out string, failed bool) map[string]any {
	if failed {
		return map[string]any{"error": out}
	}
	var value any
	if json.Unmarshal([]byte(out), &value) == nil {
		return map[string]any{"result": value}
	}
	return map[string]any{"result": out}
}

func (g *geminiBrain) post(ctx context.Context, body any, into any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1beta/models/"+g.model+":generateContent", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.key)
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gemini could not be reached: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return errQuota
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("gemini answered %d: %s", resp.StatusCode, shorten(string(payload), 300))
	}
	return json.Unmarshal(payload, into)
}
