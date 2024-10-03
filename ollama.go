package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaClient struct {
	BaseURL             string
	ConversationHistory []Message
}

func NewOllamaClient(baseURL string) *OllamaClient {
	return &OllamaClient{
		BaseURL:             baseURL,
		ConversationHistory: []Message{},
	}
}

func (oc *OllamaClient) AddSystemPrompt(systemMessage string) {
	oc.ConversationHistory = append(oc.ConversationHistory, Message{Role: "system", Content: systemMessage})
}

func (oc *OllamaClient) AddUserMessage(userMessage string) {
	oc.ConversationHistory = append(oc.ConversationHistory, Message{Role: "user", Content: userMessage})
}

func (oc *OllamaClient) AddAssistantMessage(assistantMessage string) {
	oc.ConversationHistory = append(oc.ConversationHistory, Message{Role: "assistant", Content: assistantMessage})
}

func (oc *OllamaClient) GenerateStream(model, prompt string, options map[string]interface{}, format string) error {
	oc.AddUserMessage(prompt)

	conversationContext := ""
	for _, msg := range oc.ConversationHistory {
		conversationContext += fmt.Sprintf("%s: %s | ", msg.Role, msg.Content)
	}

	payload := map[string]interface{}{
		"model":  model,
		"prompt": conversationContext,
		"stream": true,
	}
	if options != nil {
		payload["options"] = options
	}
	if format != "" {
		payload["format"] = format
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %v", err)
	}

	resp, err := http.Post(fmt.Sprintf("%s/api/generate", oc.BaseURL), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("download error: %v", err)
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	for {
		var result map[string]interface{}
		if err := decoder.Decode(&result); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("JSON decode error: %v", err)
		}

		fmt.Println("Received result:", result) // Process each part as required

		if done, ok := result["done"].(bool); ok && done {
			break
		}
		if respContent, ok := result["response"].(string); ok {
			oc.AddAssistantMessage(respContent)
		}
	}
	return nil
}

func (oc *OllamaClient) GenerateNoStream(model, prompt string, options map[string]interface{}, format string) (map[string]interface{}, error) {
	oc.AddUserMessage(prompt)

	conversationContext := ""
	for _, msg := range oc.ConversationHistory {
		conversationContext += fmt.Sprintf("%s: %s | ", msg.Role, msg.Content)
	}

	payload := map[string]interface{}{
		"model":  model,
		"prompt": conversationContext,
		"stream": false,
	}
	if options != nil {
		payload["options"] = options
	}
	if format != "" {
		payload["format"] = format
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %v", err)
	}

	resp, err := http.Post(fmt.Sprintf("%s/api/generate", oc.BaseURL), "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad response status: %s", resp.Status)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("JSON decode error: %v", err)
	}

	if done, ok := result["done"].(bool); ok && done {
		if respContent, ok := result["response"].(string); ok {
			oc.AddAssistantMessage(respContent)
		}
	}
	return result, nil
}
