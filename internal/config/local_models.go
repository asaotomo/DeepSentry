package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// LocalModelVision is the capability reported for one loaded model. Known is
// false when the runtime does not expose usable vision metadata.
type LocalModelVision struct {
	Supported bool
	Known     bool
}

// LocalModelRuntimeInfo contains settings of the loaded instance, rather than
// the model card's theoretical limits. A zero context means unavailable.
type LocalModelRuntimeInfo struct {
	ContextWindowTokens  int
	NativeToolsSupported bool
	NativeToolsKnown     bool
}

// LocalModelsURL derives the OpenAI-compatible model-list endpoint from a
// local runtime's base URL or chat endpoint, preserving reverse-proxy prefixes.
func LocalModelsURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("本地模型 API 地址须为完整的 http(s) URL")
	}
	path := strings.TrimRight(u.Path, "/")
	for _, endpoint := range []string{"/chat/completions", "/responses", "/models"} {
		if strings.HasSuffix(path, endpoint) {
			path = strings.TrimSuffix(path, endpoint)
			break
		}
	}
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	u.Path = path + "/models"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// DiscoverLocalModels lists the exact IDs served by an OpenAI-compatible
// runtime. It never substitutes a made-up default when the server is offline.
func DiscoverLocalModels(ctx context.Context, apiURL, apiKey string) ([]string, error) {
	modelsURL, err := LocalModelsURL(apiURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(apiKey); key != "" && key != "none" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := HTTPClient(4 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取本地模型列表失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取本地模型列表失败: HTTP %d", resp.StatusCode)
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("读取本地模型列表失败: %w", err)
	}
	if len(body) > maxResponse {
		return nil, fmt.Errorf("本地模型列表超过 %d 字节", maxResponse)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("本地模型列表格式无效: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	seen := make(map[string]struct{}, len(list.Data))
	for _, entry := range list.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" || len(id) > 256 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("本地服务未返回可用模型 ID；请先加载模型")
	}
	return ids, nil
}

// DiscoverLocalModelVision queries the selected model, rather than assuming
// every model served by a local runtime has the same modalities. Call this
// during setup; EffectiveModelCapabilities must remain network-free.
func DiscoverLocalModelVision(ctx context.Context, provider, apiURL, apiKey, modelID string) (LocalModelVision, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelsURL, err := LocalModelsURL(apiURL)
	if err != nil {
		return LocalModelVision{}, err
	}
	u, _ := url.Parse(modelsURL)
	method := http.MethodGet
	var payload []byte
	switch provider {
	case "ollama":
		u.Path = strings.TrimSuffix(u.Path, "/v1/models") + "/api/show"
		method = http.MethodPost
		payload, err = json.Marshal(map[string]string{"model": modelID})
		if err != nil {
			return LocalModelVision{}, err
		}
	case "lmstudio":
		u.Path = strings.TrimSuffix(u.Path, "/v1/models") + "/api/v1/models"
	case "localai":
		u.Path += "/capabilities"
	}
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return LocalModelVision{}, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(apiKey); key != "" && key != "none" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := HTTPClient(4 * time.Second).Do(req)
	if err != nil {
		return LocalModelVision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return LocalModelVision{}, fmt.Errorf("读取模型能力失败: HTTP %d", resp.StatusCode)
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return LocalModelVision{}, err
	}
	if len(body) > maxResponse {
		return LocalModelVision{}, fmt.Errorf("模型能力响应超过 %d 字节", maxResponse)
	}
	if provider == "ollama" {
		var result struct {
			Capabilities *[]string `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return LocalModelVision{}, err
		}
		if result.Capabilities != nil {
			return LocalModelVision{Supported: hasVisionCapability(*result.Capabilities, false), Known: true}, nil
		}
		return LocalModelVision{}, nil
	}
	if provider == "lmstudio" {
		var result struct {
			Models []struct {
				Key             string `json:"key"`
				LoadedInstances []struct {
					ID string `json:"id"`
				} `json:"loaded_instances"`
				Capabilities struct {
					Vision *bool `json:"vision"`
				} `json:"capabilities"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return LocalModelVision{}, err
		}
		for _, model := range result.Models {
			matched := model.Key == modelID
			for _, instance := range model.LoadedInstances {
				matched = matched || instance.ID == modelID
			}
			if matched && model.Capabilities.Vision != nil {
				return LocalModelVision{Supported: *model.Capabilities.Vision, Known: true}, nil
			}
		}
		return LocalModelVision{}, nil
	}
	var result struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return LocalModelVision{}, err
	}
	for _, model := range result.Data {
		var id string
		if err := json.Unmarshal(model["id"], &id); err != nil || id != modelID {
			continue
		}
		return visionFromModelMetadata(model, provider == "localai" || provider == "llamacpp"), nil
	}
	return LocalModelVision{}, nil
}

// DiscoverLocalModelRuntime reads local runtime settings that materially
// affect agent quality. Unsupported provider APIs return unknown fields.
func DiscoverLocalModelRuntime(ctx context.Context, provider, apiURL, apiKey, modelID string) (LocalModelRuntimeInfo, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "lmstudio" && provider != "ollama" && provider != "localai" {
		return LocalModelRuntimeInfo{}, nil
	}
	modelsURL, err := LocalModelsURL(apiURL)
	if err != nil {
		return LocalModelRuntimeInfo{}, err
	}
	u, _ := url.Parse(modelsURL)
	basePath := strings.TrimSuffix(u.Path, "/v1/models")
	if provider == "localai" {
		u.Path += "/capabilities"
		body, err := requestLocalModelMetadata(ctx, http.MethodGet, u.String(), apiKey, nil)
		if err != nil {
			return LocalModelRuntimeInfo{}, err
		}
		var result struct {
			Data []struct {
				ID           string    `json:"id"`
				Capabilities *[]string `json:"capabilities"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return LocalModelRuntimeInfo{}, err
		}
		for _, model := range result.Data {
			if model.ID != modelID || model.Capabilities == nil {
				continue
			}
			info := LocalModelRuntimeInfo{NativeToolsKnown: true}
			for _, capability := range *model.Capabilities {
				info.NativeToolsSupported = info.NativeToolsSupported || strings.EqualFold(capability, "tools")
			}
			return info, nil
		}
		return LocalModelRuntimeInfo{}, nil
	}
	if provider == "lmstudio" {
		u.Path = basePath + "/api/v1/models"
		body, err := requestLocalModelMetadata(ctx, http.MethodGet, u.String(), apiKey, nil)
		if err != nil {
			return LocalModelRuntimeInfo{}, err
		}
		var result struct {
			Models []struct {
				Key             string `json:"key"`
				LoadedInstances []struct {
					ID     string `json:"id"`
					Config struct {
						ContextLength int `json:"context_length"`
					} `json:"config"`
				} `json:"loaded_instances"`
				Capabilities struct {
					TrainedForToolUse *bool `json:"trained_for_tool_use"`
				} `json:"capabilities"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return LocalModelRuntimeInfo{}, err
		}
		for _, model := range result.Models {
			matched := model.Key == modelID
			for _, instance := range model.LoadedInstances {
				matched = matched || instance.ID == modelID
			}
			if !matched {
				continue
			}
			info := LocalModelRuntimeInfo{}
			for _, instance := range model.LoadedInstances {
				if model.Key != modelID && instance.ID != modelID {
					continue
				}
				contextLength := instance.Config.ContextLength
				if contextLength >= 4_096 && contextLength <= 4_194_304 &&
					(info.ContextWindowTokens == 0 || contextLength < info.ContextWindowTokens) {
					info.ContextWindowTokens = contextLength
				}
			}
			if model.Capabilities.TrainedForToolUse != nil {
				info.NativeToolsKnown = true
				info.NativeToolsSupported = *model.Capabilities.TrainedForToolUse
			}
			return info, nil
		}
		return LocalModelRuntimeInfo{}, nil
	}

	u.Path = basePath + "/api/ps"
	body, err := requestLocalModelMetadata(ctx, http.MethodGet, u.String(), apiKey, nil)
	if err != nil {
		return LocalModelRuntimeInfo{}, err
	}
	var running struct {
		Models []struct {
			Model         string `json:"model"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &running); err != nil {
		return LocalModelRuntimeInfo{}, err
	}
	info := LocalModelRuntimeInfo{}
	for _, model := range running.Models {
		if model.Model != modelID && model.Name != modelID {
			continue
		}
		if model.ContextLength >= 4_096 && model.ContextLength <= 4_194_304 {
			info.ContextWindowTokens = model.ContextLength
		}
		break
	}
	u.Path = basePath + "/api/show"
	payload, _ := json.Marshal(map[string]string{"model": modelID})
	body, err = requestLocalModelMetadata(ctx, http.MethodPost, u.String(), apiKey, payload)
	if err != nil {
		return info, err
	}
	var shown struct {
		Capabilities *[]string `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &shown); err != nil {
		return info, err
	}
	if shown.Capabilities != nil {
		info.NativeToolsKnown = true
		for _, capability := range *shown.Capabilities {
			info.NativeToolsSupported = info.NativeToolsSupported || strings.EqualFold(capability, "tools")
		}
	}
	return info, nil
}

// ApplyLocalRuntimeContext fills an omitted top-level context window and
// exposes tool support from the active instance. Explicit user values win.
func ApplyLocalRuntimeContext(ctx context.Context, cfg *Config) (LocalModelRuntimeInfo, error) {
	if cfg == nil || !IsLocalProvider(cfg.Provider) || len(cfg.Models) > 0 {
		return LocalModelRuntimeInfo{}, nil
	}
	if cfg.ContextWindowTokens > 0 && cfg.UseNativeTools {
		return LocalModelRuntimeInfo{}, nil
	}
	info, err := DiscoverLocalModelRuntime(ctx, cfg.Provider, cfg.ApiURL, cfg.ApiKey, cfg.ModelName)
	cfg.LocalNativeToolsAvailable = info.NativeToolsKnown && info.NativeToolsSupported
	if cfg.ContextWindowTokens <= 0 && info.ContextWindowTokens > 0 {
		cfg.ContextWindowTokens = info.ContextWindowTokens
		cfg.LocalContextDiscovered = true
	}
	return info, err
}

func requestLocalModelMetadata(ctx context.Context, method, endpoint, apiKey string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(apiKey); key != "" && key != "none" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := HTTPClient(4 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取本地模型配置失败: HTTP %d", resp.StatusCode)
	}
	const maxResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponse {
		return nil, fmt.Errorf("本地模型配置响应超过 %d 字节", maxResponse)
	}
	return body, nil
}

func hasVisionCapability(values []string, inputModalities bool) bool {
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "vision":
			return true
		case "image":
			if inputModalities {
				return true
			}
		}
	}
	return false
}

func visionFromModelMetadata(model map[string]json.RawMessage, listAuthoritative bool) LocalModelVision {
	if raw := model["vision"]; len(raw) > 0 {
		var value bool
		if json.Unmarshal(raw, &value) == nil {
			return LocalModelVision{Supported: value, Known: true}
		}
	}
	knownNegative := false
	if raw := model["multimodal"]; len(raw) > 0 {
		var value bool
		if json.Unmarshal(raw, &value) == nil && !value {
			knownNegative = true
		}
	}
	for _, key := range []string{"capabilities", "input_modalities"} {
		raw := model[key]
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			if hasVisionCapability(list, key == "input_modalities") {
				return LocalModelVision{Supported: true, Known: true}
			}
			ambiguousMultimodal := false
			for _, capability := range list {
				ambiguousMultimodal = ambiguousMultimodal || strings.EqualFold(strings.TrimSpace(capability), "multimodal")
			}
			knownNegative = knownNegative || (listAuthoritative && !ambiguousMultimodal)
			continue
		}
		var object map[string]bool
		if json.Unmarshal(raw, &object) == nil {
			if object["vision"] || (key == "input_modalities" && object["image"]) {
				return LocalModelVision{Supported: true, Known: true}
			}
			if _, exists := object["vision"]; exists {
				knownNegative = true
			} else if _, exists := object["image"]; exists && key == "input_modalities" {
				knownNegative = true
			}
		}
	}
	return LocalModelVision{Known: knownNegative}
}
