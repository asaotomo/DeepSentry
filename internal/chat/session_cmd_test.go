package chat

import "testing"

func TestNormalizeAndMatchSessionTalk(t *testing.T) {
	if got := normalizeSessionTalk(" 帮我重启会话。 "); got != "重启会话" {
		t.Fatalf("normalize=%q", got)
	}
	if !isRestartTalk("重启会话") || !isRestartTalk("new session") || isRestartTalk("重启服务器") {
		t.Fatal("restart intent mismatch")
	}
	if _, ok := matchSwitchTalk("切换到生产环境"); ok {
		t.Fatal("unrelated 切换 should not list sessions")
	}
	rest, ok := matchSwitchTalk("切换会话 2")
	if !ok || rest != "2" {
		t.Fatalf("switch rest=%q ok=%v", rest, ok)
	}
	n, ok := parsePickIndex("第2个")
	if !ok || n != 2 {
		t.Fatalf("pick=%d ok=%v", n, ok)
	}
}
