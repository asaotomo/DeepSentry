package analyzer

import (
	"ai-edr/internal/config"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeTestPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evidence.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func imageMessage(t *testing.T) Message {
	t.Helper()
	attachment, err := PrepareImageAttachment(writeTestPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	return Message{Role: "user", Content: "分析截图", Attachments: []ImageAttachment{attachment}}
}

func TestOpenAIChatSerializesPathBackedImageDataURL(t *testing.T) {
	message := imageMessage(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		messages := raw["messages"].([]interface{})
		content := messages[0].(map[string]interface{})["content"].([]interface{})
		if content[0].(map[string]interface{})["type"] != "text" || content[1].(map[string]interface{})["type"] != "image_url" {
			t.Fatalf("unexpected OpenAI content blocks: %#v", content)
		}
		imageURL := content[1].(map[string]interface{})["image_url"].(map[string]interface{})["url"].(string)
		if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
			t.Fatalf("image data URL missing: %.40q", imageURL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	if _, err := callOpenAICompatible(context.Background(), config.Config{ApiURL: server.URL, ModelName: "vision-test", ApiKey: "none"}, []Message{message}, false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesAndAnthropicSerializeImagesUsingNativeBlockShapes(t *testing.T) {
	message := imageMessage(t)
	t.Run("responses", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			input := raw["input"].([]interface{})
			blocks := input[0].(map[string]interface{})["content"].([]interface{})
			imageBlock := blocks[len(blocks)-1].(map[string]interface{})
			if imageBlock["type"] != "input_image" || imageBlock["detail"] != "auto" {
				t.Fatalf("Responses input_image block missing: %#v", blocks)
			}
			_, _ = w.Write([]byte(`{"output_text":"ok"}`))
		}))
		defer server.Close()
		if _, err := callOpenAIResponses(context.Background(), config.Config{ApiURL: server.URL + "/responses", ModelName: "gpt-vision", ApiKey: "none"}, []Message{message}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			messages := raw["messages"].([]interface{})
			blocks := messages[0].(map[string]interface{})["content"].([]interface{})
			imageBlock := blocks[1].(map[string]interface{})
			source := imageBlock["source"].(map[string]interface{})
			if imageBlock["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" {
				t.Fatalf("Anthropic image block invalid: %#v", imageBlock)
			}
			if _, err := base64.StdEncoding.DecodeString(source["data"].(string)); err != nil {
				t.Fatalf("Anthropic image data is not base64: %v", err)
			}
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
		}))
		defer server.Close()
		if _, err := callAnthropic(context.Background(), config.Config{ApiURL: server.URL + "/v1/messages", APIProtocol: config.ProtocolAnthropicMessages, ModelName: "claude-vision", ApiKey: "test"}, []Message{message}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVisionRoutingSkipsTextOnlyModelAndUsesVisionFallback(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	var textHits atomic.Int32
	textServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		textHits.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer textServer.Close()
	visionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"vision-ok"}}]}`))
	}))
	defer visionServer.Close()
	config.GlobalConfig = config.Config{
		Provider: "custom", APIProtocol: "openai_chat", ApiKey: "none",
		Models: []config.ModelConfig{
			{ID: "text", Role: "primary", Provider: "custom", APIProtocol: "openai_chat", APIURL: textServer.URL, ModelName: "text-only", VisionMode: "disabled"},
			{ID: "vision", Role: "fallback", Provider: "custom", APIProtocol: "openai_chat", APIURL: visionServer.URL, ModelName: "deepseek-v4-flash-vision-exp", VisionMode: "auto"},
		},
	}
	result, err := CallLLMWithRetryContext(context.Background(), []Message{imageMessage(t)}, false, nil)
	if err != nil || result.Content != "vision-ok" || result.ModelID != "vision" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if textHits.Load() != 0 {
		t.Fatalf("text-only model received an image request %d times", textHits.Load())
	}
}

func TestLocalVisualModelWithNonVisualNameSendsImageWhenEnabled(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	var received atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		messages, ok := raw["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Errorf("missing messages: %#v", raw)
			return
		}
		content, ok := messages[0].(map[string]interface{})["content"].([]interface{})
		if !ok || len(content) < 2 || content[1].(map[string]interface{})["type"] != "image_url" {
			t.Errorf("image was not sent: %#v", messages)
			return
		}
		received.Store(true)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"image-ok"}}]}`))
	}))
	defer server.Close()
	config.GlobalConfig = config.Config{
		Provider: "ollama", APIProtocol: "openai_chat", ApiURL: server.URL,
		ApiKey: "none", ModelName: "gemma4:latest", VisionMode: "enabled",
	}
	result, err := CallLLMWithRetryContext(context.Background(), []Message{imageMessage(t)}, false, nil)
	if err != nil || result.Content != "image-ok" || !received.Load() {
		t.Fatalf("local visual model result=%#v received=%v err=%v", result, received.Load(), err)
	}
}
