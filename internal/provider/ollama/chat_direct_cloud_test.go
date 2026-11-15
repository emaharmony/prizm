package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
)

type directCloudRoundTripper func(*http.Request) (*http.Response, error)

func (f directCloudRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDirectCloudChatProviderUsesBearerAndDirectFlashWireModel(t *testing.T) {
	const secret = "synthetic-api-key"
	chat, err := NewDirectCloudChatProvider(secret)
	if err != nil {
		t.Fatal("create direct Cloud chat provider")
	}
	var gotModel string
	chat.cloudClient.Transport = directCloudRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "ollama.com" || request.URL.Path != "/api/chat" {
			t.Fatalf("unexpected Cloud endpoint: %s", request.URL.Redacted())
		}
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatal("Cloud bearer header missing")
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal("decode synthetic Cloud request")
		}
		gotModel, _ = payload["model"].(string)
		if gotModel != "glm-5.3-flash" {
			t.Fatal("direct Cloud model alias was not applied")
		}
		if _, present := payload["format"]; present {
			t.Fatal("direct Flash request unexpectedly contains format")
		}
		body, err := json.Marshal(payload)
		if err != nil || strings.Contains(string(body), secret) {
			t.Fatal("Cloud key appeared in request body")
		}
		responseBody := `{"model":"glm-5.3-flash","done":true,"message":{"role":"assistant","content":"IDEA: bounded synthetic response"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	})
	for range 2 {
		response, err := chat.ChatGenerate(context.Background(), provider.ChatGenerateRequest{
			Model: "glm-5.3-flash:cloud", Format: "json", OutputSchema: map[string]any{"type": "object"},
			Messages: []provider.ChatMessage{{Role: "user", Content: "synthetic"}}, MaxTokens: 128,
		})
		if err != nil || gotModel != "glm-5.3-flash" || response.Model != "glm-5.3-flash:cloud" {
			t.Fatal("repeated direct Cloud request did not retain authentication and configured model")
		}
	}
}

func TestDirectCloudChatProviderBlocksRedirectAndKeepsKeyOutOfErrorsAndLogs(t *testing.T) {
	const secret = "synthetic-api-key"
	chat, err := NewDirectCloudChatProvider(secret)
	if err != nil {
		t.Fatal("create direct Cloud chat provider")
	}
	requests := 0
	chat.cloudClient.Transport = directCloudRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://attacker.invalid/api/chat"}},
			Body: io.NopCloser(strings.NewReader(secret)), Request: request}, nil
	})
	var logs bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLog) })
	_, err = chat.ChatGenerate(context.Background(), provider.ChatGenerateRequest{Model: "glm-5.3-flash:cloud"})
	if err == nil || requests != 1 || strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatal("redirect handling leaked the key or issued a redirected request")
	}
	chat.BaseURL = "https://attacker.invalid"
	_, err = chat.ChatGenerate(context.Background(), provider.ChatGenerateRequest{Model: "glm-5.3-flash:cloud"})
	if err == nil || requests != 1 {
		t.Fatal("direct Cloud provider accepted an alternate host")
	}
}

func TestDirectCloudChatProviderRejectsNonFlashModelAndMissingKey(t *testing.T) {
	if _, err := NewDirectCloudChatProvider(" \r\n "); err == nil || strings.Contains(err.Error(), " \r\n ") {
		t.Fatal("missing key was accepted or echoed")
	}
	chat, err := NewDirectCloudChatProvider("synthetic-api-key")
	if err != nil {
		t.Fatal("create direct Cloud chat provider")
	}
	_, err = chat.ChatGenerate(context.Background(), provider.ChatGenerateRequest{Model: "glm-5.3:cloud"})
	if err == nil {
		t.Fatal("direct Cloud provider accepted a non-Flash model")
	}
}

func TestLocalChatProviderKeepsLocalModelAndNoCloudAuthorization(t *testing.T) {
	chat := NewChatProvider(DefaultBaseURL)
	cases := []struct {
		model      string
		format     string
		wantFormat bool
	}{
		{model: "glm-5.3-flash:cloud", format: "json", wantFormat: false},
		{model: "glm-5.3:cloud", format: "json", wantFormat: true},
	}
	requestIndex := 0
	chat.HTTPClient.Transport = directCloudRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != DefaultBaseURL+"/api/chat" || request.Header.Get("Authorization") != "" {
			t.Fatal("local Ollama request used a Cloud endpoint or credential")
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal("decode local Ollama request")
		}
		current := cases[requestIndex]
		requestIndex++
		if payload["model"] != current.model {
			t.Fatal("local Ollama model name was translated")
		}
		_, hasFormat := payload["format"]
		if hasFormat != current.wantFormat {
			t.Fatal("local Ollama format behavior changed")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"model":"` + current.model + `","done":true,"message":{"role":"assistant","content":"OK"}}`)), Request: request}, nil
	})
	for _, current := range cases {
		_, err := chat.ChatGenerate(context.Background(), provider.ChatGenerateRequest{
			Model: current.model, Format: current.format, Messages: []provider.ChatMessage{{Role: "user", Content: "synthetic"}},
		})
		if err != nil {
			t.Fatal("in-memory local Ollama request failed")
		}
	}
	if requestIndex != len(cases) {
		t.Fatal("in-memory local Ollama requests were incomplete")
	}
}
