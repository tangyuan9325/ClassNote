package config

import "testing"

// TestNormalizeGitHub 覆盖更新面板会写回的几个字段。
// 这些值会直接拼进下载地址，形状不对必须在落盘前被清掉。
func TestNormalizeGitHub(t *testing.T) {
	g := GitHubConfig{
		Mirror: "  gh fast  ",
		Mirrors: []string{
			"https://a.example/",
			"https://a.example", // 补斜杠后与上一条重复
			"http://plain.example/",
			"  https://b.example/  ",
			"ftp://c.example/",
			"", "https://d.example/", "https://e.example/", "https://f.example/", "https://g.example/",
		},
		LastNode: "  gh-proxy.com ",
	}
	normalizeGitHub(&g)

	if g.Mirror != "" {
		t.Fatalf("带空格的节点 ID 应回落自动，实际 %q", g.Mirror)
	}
	if g.LastNode != "gh-proxy.com" {
		t.Fatalf("LastNode 应去掉空白，实际 %q", g.LastNode)
	}
	want := []string{"https://a.example/", "https://b.example/", "https://d.example/", "https://e.example/", "https://f.example/"}
	if len(g.Mirrors) != len(want) {
		t.Fatalf("自定义节点应剩 %d 条，实际 %d：%v", len(want), len(g.Mirrors), g.Mirrors)
	}
	for i, w := range want {
		if g.Mirrors[i] != w {
			t.Fatalf("第 %d 条应为 %q，实际 %q", i, w, g.Mirrors[i])
		}
	}
}

// TestNormalizeGitHubKeepsPresets 确认 auto/off 与合法 ID 不被误伤。
func TestNormalizeGitHubKeepsPresets(t *testing.T) {
	for _, m := range []string{"auto", "off", "ghfast", "custom-3"} {
		g := GitHubConfig{Mirror: m}
		normalizeGitHub(&g)
		if g.Mirror != m {
			t.Errorf("%q 不该被清掉，实际 %q", m, g.Mirror)
		}
	}
	// 超长或带斜杠的 ID 一律视为无效。
	for _, m := range []string{"a/../../b", "x y", "0123456789012345678901234567890123456789"} {
		g := GitHubConfig{Mirror: m}
		normalizeGitHub(&g)
		if g.Mirror != "" {
			t.Errorf("%q 应被清掉，实际 %q", m, g.Mirror)
		}
	}
}
