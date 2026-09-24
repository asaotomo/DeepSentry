package chat

import (
	"encoding/json"
	"net/url"
	"strings"
)

const weixinCDNBase = "https://novac2c.cdn.weixin.qq.com/c2c"

type weixinCDN struct {
	URL   string `json:"full_url"`
	Query string `json:"encrypt_query_param"`
	Key   string `json:"aes_key"`
}
type weixinItem struct {
	Type int `json:"type"`
	Text struct {
		Text string `json:"text"`
	} `json:"text_item"`
	Image struct {
		Media weixinCDN `json:"media"`
		Thumb weixinCDN `json:"thumb_media"`
		Key   string    `json:"aeskey"`
		URL   string    `json:"url"`
	} `json:"image_item"`
	Voice struct {
		Media    weixinCDN `json:"media"`
		Text     string    `json:"text"`
		Encoding int       `json:"encode_type"`
	} `json:"voice_item"`
	File struct {
		Media weixinCDN `json:"media"`
		Name  string    `json:"file_name"`
		MD5   string    `json:"md5"`
		Len   string    `json:"len"`
	} `json:"file_item"`
	Video struct {
		Media weixinCDN `json:"media"`
	} `json:"video_item"`
	Ref struct {
		Item *weixinItem `json:"message_item"`
	} `json:"ref_msg"`
}
type weixinInbound struct {
	ID      json.RawMessage `json:"message_id"`
	From    string          `json:"from_user_id"`
	To      string          `json:"to_user_id"`
	Group   string          `json:"group_id"`
	Type    int             `json:"message_type"`
	State   int             `json:"message_state"`
	Context string          `json:"context_token"`
	Items   []weixinItem    `json:"item_list"`
}

func appendUniqueURL(dst []string, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return dst
	}
	for _, e := range dst {
		if e == raw {
			return dst
		}
	}
	return append(dst, raw)
}
func weixinCDNURLs(m weixinCDN) []string {
	var out []string
	if m.Query != "" {
		out = appendUniqueURL(out, weixinCDNBase+"/download?encrypted_query_param="+url.QueryEscape(m.Query))
	}
	return appendUniqueURL(out, m.URL)
}
func weixinImageAttachment(img struct {
	Media weixinCDN `json:"media"`
	Thumb weixinCDN `json:"thumb_media"`
	Key   string    `json:"aeskey"`
	URL   string    `json:"url"`
}) (Attachment, bool) {
	urls := weixinCDNURLs(img.Media)
	for _, u := range weixinCDNURLs(img.Thumb) {
		urls = appendUniqueURL(urls, u)
	}
	urls = appendUniqueURL(urls, img.URL)
	if len(urls) == 0 {
		return Attachment{}, false
	}
	a := Attachment{Kind: "image", URL: urls[0], Fallbacks: append([]string(nil), urls[1:]...)}
	if key := firstNonEmpty(img.Key, img.Media.Key, img.Thumb.Key); key != "" {
		a.Key = key
		a.Cipher = "ecb"
	}
	return a, true
}
func weixinVoiceAttachment(voice struct {
	Media    weixinCDN `json:"media"`
	Text     string    `json:"text"`
	Encoding int       `json:"encode_type"`
}) Attachment {
	name := "voice.bin"
	switch voice.Encoding {
	case 5:
		name = "voice.amr"
	case 6:
		name = "voice.silk"
	case 7:
		name = "voice.mp3"
	case 8:
		name = "voice.ogg"
	}
	urls := weixinCDNURLs(voice.Media)
	a := Attachment{Kind: "audio", Name: name, Transcript: voice.Text}
	if len(urls) > 0 {
		a.URL = urls[0]
		a.Fallbacks = append([]string(nil), urls[1:]...)
	}
	if voice.Media.Key != "" {
		a.Key = voice.Media.Key
		a.Cipher = "ecb"
	}
	return a
}
func weixinFileAttachment(kind, name string, media weixinCDN) (Attachment, bool) {
	urls := weixinCDNURLs(media)
	if len(urls) == 0 {
		return Attachment{}, false
	}
	a := Attachment{Kind: kind, Name: name, URL: urls[0], Fallbacks: append([]string(nil), urls[1:]...)}
	if media.Key != "" {
		a.Key = media.Key
		a.Cipher = "ecb"
	}
	return a, true
}
func parseWeixinItems(items []weixinItem) (string, []Attachment) {
	var texts []string
	var attachments []Attachment
	var refs []weixinItem
	hasMedia := false
	for _, i := range items {
		if i.Ref.Item != nil {
			refs = append(refs, *i.Ref.Item)
		}
		switch i.Type {
		case 1:
			texts = append(texts, i.Text.Text)
		case 2:
			if a, ok := weixinImageAttachment(i.Image); ok {
				attachments = append(attachments, a)
				hasMedia = true
			}
		case 3:
			attachments = append(attachments, weixinVoiceAttachment(i.Voice))
			hasMedia = true
		case 4:
			if a, ok := weixinFileAttachment("file", firstNonEmpty(i.File.Name, "file.bin"), i.File.Media); ok {
				attachments = append(attachments, a)
				hasMedia = true
			}
		case 5:
			if a, ok := weixinFileAttachment("file", "video.bin", i.Video.Media); ok {
				attachments = append(attachments, a)
				hasMedia = true
			}
		default:
			attachments = append(attachments, Attachment{Kind: "unsupported"})
		}
	}
	if !hasMedia {
		for _, r := range refs {
			switch r.Type {
			case 2:
				if a, ok := weixinImageAttachment(r.Image); ok {
					attachments = append(attachments, a)
				}
			case 4:
				if a, ok := weixinFileAttachment("file", firstNonEmpty(r.File.Name, "file.bin"), r.File.Media); ok {
					attachments = append(attachments, a)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), attachments
}
func weixinShouldHandle(m weixinInbound, botID string) bool {
	return m.Type == 1 && m.From != "" && m.From != botID && m.State != 1
}
func coalesceWeixinInbound(msgs []weixinInbound) []weixinInbound {
	var out []weixinInbound
	for _, m := range msgs {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if prev.Type == 1 && m.Type == 1 && prev.From == m.From && prev.Group == m.Group && prev.From != "" {
				prev.Items = append(prev.Items, m.Items...)
				if len(m.ID) > 0 {
					prev.ID = m.ID
				}
				if m.Context != "" {
					prev.Context = m.Context
				}
				continue
			}
		}
		out = append(out, m)
	}
	return out
}
