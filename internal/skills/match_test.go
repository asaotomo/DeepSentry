package skills

import (
	"strings"
	"testing"
)

func TestMatchLoadsBilibiliZipAndFofaFromNaturalQueries(t *testing.T) {
	catalog, err := LoadCatalog([]string{"../../skills"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		query string
		want  string
	}{
		{"用MCP打开B站播放杰克奥特曼第二集，2倍速并全屏", "bilibili-play"},
		{"test1应该是伪加密，帮我解开这个 zip", "zipcracker"},
		{"解开 test04.zip", "zipcracker"},
		{"这个 zip 打不开", "zipcracker"},
		{"python3 ZipCracker.py test04.zip -m '?uali?s?d?d?d'", "zipcracker"},
		{"帮我解密 build/flag.zip", "zipcracker"},
		{"用 FOFA 查一下公网暴露的致远 OA", "fofamap"},
		{"用 FOFA 查一下公网暴露的致远 OA。先看账户和规则再搜索，不要主动扫描、不要 nuclei。只返回前几条摘要（ip/port/title），不要导出全量。", "fofamap"},
	}
	for _, test := range tests {
		matches := catalog.Match(test.query, 2)
		if len(matches) == 0 || !strings.EqualFold(matches[0].Meta.Name, test.want) {
			t.Fatalf("query %q matched %#v, want %s first", test.query, matches, test.want)
		}
		if matches[0].Score < autoLoadMinScore {
			t.Fatalf("query %q score=%d, want >= %d", test.query, matches[0].Score, autoLoadMinScore)
		}
	}
}

func TestMatchFOFABilibiliQueryDoesNotLoadPlaySkill(t *testing.T) {
	catalog, err := LoadCatalog([]string{"../../skills"})
	if err != nil {
		t.Fatal(err)
	}
	query := "用 FofaMap MCP 做能力体检。fofa_icon_search url=https://www.bilibili.com size=3；fofa_agent_run intent=查找哔哩哔哩官网候选。禁止 nuclei。"
	matches := catalog.Match(query, 2)
	if len(matches) == 0 || !strings.EqualFold(matches[0].Meta.Name, "fofamap") {
		t.Fatalf("FOFA bili query matched %#v, want fofamap first", matches)
	}
	for _, match := range matches {
		if strings.EqualFold(match.Meta.Name, "bilibili-play") {
			t.Fatalf("FOFA asset task should not auto-load bilibili-play: %#v", matches)
		}
	}
}

func TestLooksLikeZIPRecoverIgnoresDownloadOnly(t *testing.T) {
	if LooksLikeZIPRecover("把这个 zip 下载到本地备份") {
		t.Fatal("download-only zip mention should not count as recover")
	}
	if !LooksLikeZIPRecover("解开 test04.zip") {
		t.Fatal("解开 test04.zip should count as recover")
	}
	if got := ExtractZIPSources("请处理 build/test04.zip 和 /tmp/flag.ZIP"); len(got) != 2 {
		t.Fatalf("ExtractZIPSources=%#v", got)
	}
}

func TestMatchIgnoresGenericChitChat(t *testing.T) {
	catalog := &SkillCatalog{Skills: []SkillMeta{
		{Name: "forensics", Description: "取证分析、排查主机异常进程", AllowImplicit: true},
		{Name: "log-analysis", Description: "日志关联、攻击 IP 分析", AllowImplicit: true},
	}}
	if matches := catalog.Match("你好，你能做什么", 2); len(matches) != 0 {
		t.Fatalf("generic chat should not auto-load skills: %#v", matches)
	}
}

func TestMatchHonorsAllowImplicit(t *testing.T) {
	catalog := &SkillCatalog{Skills: []SkillMeta{
		{Name: "bilibili-play", Description: "B站 播放 倍速 全屏", AllowImplicit: false},
	}}
	if matches := catalog.Match("打开B站播放奥特曼", 2); len(matches) != 0 {
		t.Fatalf("explicit-only skill leaked into match: %#v", matches)
	}
}
