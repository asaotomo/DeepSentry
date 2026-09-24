package analyzer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxImagesPerMessage = 8
	MaxImageBytes       = int64(20 << 20)
	MaxImageBatchBytes  = int64(40 << 20)
)

type openAIChatMessage struct {
	ID               string      `json:"id,omitempty"`
	Role             string      `json:"role"`
	Content          interface{} `json:"content,omitempty"`
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	Name             string      `json:"name,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type materializedImage struct {
	Attachment ImageAttachment
	Data       []byte
}

// PrepareImageAttachment validates an image without retaining its bytes in
// memory or history. Paths are absolute so resumed sessions remain stable.
func PrepareImageAttachment(path string) (ImageAttachment, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ImageAttachment{}, fmt.Errorf("图片路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("解析图片路径失败: %w", err)
	}
	file, err := os.Open(abs)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("打开图片失败: %w", err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("读取图片属性失败: %w", err)
	}
	if !stat.Mode().IsRegular() {
		return ImageAttachment{}, fmt.Errorf("图片必须是普通文件: %s", abs)
	}
	if stat.Size() <= 0 || stat.Size() > MaxImageBytes {
		return ImageAttachment{}, fmt.Errorf("图片大小必须在 1 字节到 %d MiB 之间", MaxImageBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("读取图片失败: %w", err)
	}
	mediaType, err := validatedImageMediaType(data)
	if err != nil {
		return ImageAttachment{}, err
	}
	sum := sha256.Sum256(data)
	return ImageAttachment{
		Path: abs, Name: filepath.Base(abs), MediaType: mediaType,
		Size: stat.Size(), SHA256: hex.EncodeToString(sum[:]), Detail: "auto",
	}, nil
}

func MessagesHaveImages(messages []Message) bool {
	for _, message := range messages {
		if len(message.Attachments) > 0 {
			return true
		}
	}
	return false
}

func materializeImages(attachments []ImageAttachment) ([]materializedImage, error) {
	if len(attachments) > MaxImagesPerMessage {
		return nil, fmt.Errorf("单条消息最多允许 %d 张图片", MaxImagesPerMessage)
	}
	out := make([]materializedImage, 0, len(attachments))
	var total int64
	for _, attachment := range attachments {
		prepared, err := PrepareImageAttachment(attachment.Path)
		if err != nil {
			return nil, err
		}
		if attachment.Detail != "" {
			prepared.Detail = attachment.Detail
		}
		if attachment.SHA256 != "" && !strings.EqualFold(attachment.SHA256, prepared.SHA256) {
			return nil, fmt.Errorf("图片在加入会话后已发生变化: %s", prepared.Path)
		}
		total += prepared.Size
		if total > MaxImageBatchBytes {
			return nil, fmt.Errorf("单条消息图片总大小不能超过 %d MiB", MaxImageBatchBytes>>20)
		}
		data, err := os.ReadFile(prepared.Path)
		if err != nil {
			return nil, fmt.Errorf("读取图片失败: %w", err)
		}
		out = append(out, materializedImage{Attachment: prepared, Data: data})
	}
	return out, nil
}

func validatedImageMediaType(data []byte) (string, error) {
	if len(data) < 12 {
		return "", fmt.Errorf("图片内容过短或格式无效")
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mediaType, nil
	default:
		return "", fmt.Errorf("不支持的图片格式 %q；仅支持 PNG/JPEG/GIF/WebP", mediaType)
	}
}

func buildOpenAIChatMessages(messages []Message) ([]openAIChatMessage, error) {
	out := make([]openAIChatMessage, 0, len(messages))
	for _, message := range messages {
		converted := openAIChatMessage{
			ID: message.ID, Role: message.Role, Content: message.Content,
			ReasoningContent: message.ReasoningContent, ToolCalls: message.ToolCalls,
			ToolCallID: message.ToolCallID, Name: message.Name,
		}
		if len(message.Attachments) > 0 {
			if message.Role != "user" {
				return nil, fmt.Errorf("图片附件只能出现在 user 消息中，当前 role=%s", message.Role)
			}
			images, err := materializeImages(message.Attachments)
			if err != nil {
				return nil, err
			}
			parts := make([]openAIContentPart, 0, len(images)+1)
			text := strings.TrimSpace(message.Content)
			if text == "" {
				text = "请分析所附图片，并结合当前任务继续。"
			}
			parts = append(parts, openAIContentPart{Type: "text", Text: text})
			for _, image := range images {
				detail := image.Attachment.Detail
				if detail == "" {
					detail = "auto"
				}
				parts = append(parts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{
					URL:    "data:" + image.Attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
					Detail: detail,
				}})
			}
			converted.Content = parts
		}
		out = append(out, converted)
	}
	return out, nil
}

func attachmentSummary(attachments []ImageAttachment) string {
	if len(attachments) == 0 {
		return ""
	}
	names := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = filepath.Base(attachment.Path)
		}
		names = append(names, name)
	}
	return fmt.Sprintf("[图片附件 %d 张: %s]", len(attachments), strings.Join(names, ", "))
}
