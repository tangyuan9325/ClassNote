package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeMigratesV1(t *testing.T) {
	var p Plugin
	raw := []byte(`{"id":"poem","name":"古诗","prompt":"按意象分析","tools":["lesson.read_page","notes.append"],"enabled":true}`)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Normalize() {
		t.Fatal("v1 plugin should be migrated")
	}
	if p.SchemaVersion != SchemaVersion || p.Kind != KindSkill {
		t.Fatalf("bad migration: %+v", p)
	}
	if len(p.Capabilities) == 0 || len(p.Permissions) == 0 {
		t.Fatalf("capabilities/permissions should be derived: %+v", p)
	}
	if !p.Allowed(PermWriteLesson) {
		t.Fatalf("notes.append should grant write:lesson: %v", p.Permissions)
	}
	if p.Allowed(PermWriteReport) {
		t.Fatalf("should not grant write:report: %v", p.Permissions)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []Plugin{
		{ID: "../../etc/passwd", Name: "x"},
		{ID: "ok", Name: ""},
		{ID: "ok", Name: "x", Kind: "native"},
		{ID: "ok", Name: "x", Kind: KindSkill, Tools: []string{"shell.exec"}},
		{ID: "ok", Name: "x", Kind: KindSkill, Permissions: []string{"root"}},
		{ID: "ok", Name: "x", Kind: KindSkill, UI: &UISpec{Slot: "fullscreen"}},
		{ID: "ok", Name: "x", Kind: KindSkill, UI: &UISpec{Slot: SlotAgent, Href: "javascript:alert(1)"}},
		{ID: "ok", Name: "x", Kind: KindExternal},
		{ID: "ok", Name: "x", Kind: KindSkill, Events: []string{"onBoot"}},
	}
	for i, p := range cases {
		p.SchemaVersion = SchemaVersion
		if p.Kind == KindSkill && p.Prompt == "" && len(p.Tools) == 0 && p.UI == nil {
			p.Prompt = "x"
		}
		if err := p.Validate(); err == nil {
			t.Errorf("case %d should fail: %+v", i, p)
		}
	}
}

// freshRegistry 打开一个空插件仓库（Open 会写入内置示例，这里先清掉）。
func freshRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.List() {
		if p.BuiltIn {
			if err := r.Delete(p.ID); err != nil {
				t.Fatalf("delete seed: %v", err)
			}
		}
	}
	return r, dir
}

func TestSaveAndSnapshot(t *testing.T) {
	r, _ := freshRegistry(t)
	p := Plugin{
		ID: "slot-demo", Name: "状态条演示", Kind: KindUI, Version: "1.0.0", Enabled: true,
		Prompt: "在状态条上显示本课提示",
		UI:     &UISpec{Slot: SlotStatusbar, Title: "专注模式", Body: "已开启", Order: 1},
	}
	if err := r.Save(p); err != nil {
		t.Fatalf("save: %v", err)
	}
	snap := r.Snapshot()
	if snap.Enabled == 0 || len(snap.Slots) != 1 {
		t.Fatalf("snapshot wrong: %+v", snap)
	}
	if snap.Slots[0].Slot != SlotStatusbar || snap.Slots[0].PluginID != "slot-demo" {
		t.Fatalf("slot binding wrong: %+v", snap.Slots)
	}
	if !containsStr(snap.Prompt, "状态条演示") {
		t.Fatalf("prompt should include plugin: %q", snap.Prompt)
	}

	// 停用后快照必须干净，且 generation 要变（前端靠它判断是否需要重取）。
	gen := snap.Generation
	r.SetEnabled("slot-demo", false)
	snap2 := r.Snapshot()
	if snap2.Enabled != 0 || len(snap2.Slots) != 0 || snap2.Prompt != "" {
		t.Fatalf("disabled plugin still visible: %+v", snap2)
	}
	if snap2.Generation == gen {
		t.Fatal("generation should change after reload")
	}
}

func TestValidIDBlocksTraversal(t *testing.T) {
	for _, bad := range []string{"../config", "..", "a/b", "a\\b", "", "CON", "x y"} {
		if validID(bad) {
			t.Errorf("%q should be rejected as id", bad)
		}
	}
	for _, good := range []string{"a", "a.b-c_d", "0abc"} {
		if !validID(good) {
			t.Errorf("%q should be accepted", good)
		}
	}
}

func TestDeleteOutsideDirRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "secret.json")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	defer os.Remove(outside)
	r, _ := Open(dir)
	if err := r.Delete("../secret"); err == nil {
		t.Fatal("delete should refuse traversal id")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("file outside plugin dir must remain")
	}
}

func TestMarketChannels(t *testing.T) {
	dir := t.TempDir()
	r, _ := Open(dir)

	res := r.Market(ChannelClassNote, "", "", 200, 2000, "", "")
	if res.Total == 0 || len(res.Items) == 0 {
		t.Fatalf("classnote market empty: %+v", res)
	}
	if len(res.Channels) != 2 {
		t.Fatalf("expect 2 channels, got %d", len(res.Channels))
	}

	dsh := r.Market(ChannelDSH, "记忆", "", 200, 50, "", "")
	if dsh.Channel != ChannelDSH {
		t.Fatalf("channel: %s", dsh.Channel)
	}
	if dsh.Total == 0 || len(dsh.Items) == 0 {
		t.Fatalf("dsh index should be embedded and searchable: %+v", dsh)
	}
	for _, it := range dsh.Items[:10] {
		if it.URL == "" || it.Kind != KindExternal {
			t.Fatalf("dsh item should carry url & external kind: %+v", it)
		}
	}

	cat := r.Market(ChannelDSH, "", "memory", 200, 20, "", "")
	for _, it := range cat.Items {
		if it.Category != "memory" {
			t.Fatalf("category filter leaked: %+v", it)
		}
	}
}

func TestInstallExternalKeepsDisabled(t *testing.T) {
	r, _ := freshRegistry(t)
	p, err := r.Install(MarketItem{
		ID: "demo.dsh-plugin", Name: "demo/dsh-plugin", Kind: KindExternal,
		URL: "https://github.com/demo/dsh-plugin", Version: "0.1.0",
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if p.Enabled {
		t.Fatal("external plugin must not be enabled")
	}
	if p.Prompt != "" {
		t.Fatal("external plugin must not inject prompt")
	}
	snap := r.Snapshot()
	if snap.Enabled != 0 || snap.Prompt != "" {
		t.Fatalf("external plugin leaked into prompt: %+v", snap)
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"Demo/DSH-Plugin": "demo.dsh-plugin",
		"  x  ":           "x",
		"&#!":             "plugin",
	}
	for in, want := range cases {
		if got := SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q)=%q want %q", in, got, want)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestSafeIndexURL(t *testing.T) {
	bad := []string{
		"", "http://example.com/a.json", "https://127.0.0.1/a.json",
		"https://localhost/a.json", "https://192.168.1.9/a.json",
		"https://10.0.0.1/a.json", "https://user:pass@example.com/a.json",
		"file:///etc/passwd",
	}
	for _, u := range bad {
		if safeIndexURL(u) {
			t.Errorf("%q should be refused", u)
		}
	}
	good := []string{
		"https://raw.githubusercontent.com/tangyuan9325/ClassNote/main/skills.json",
		"https://cdn.jsdelivr.net/npm/dsh-plugin-catalog/index.json",
	}
	for _, u := range good {
		if !safeIndexURL(u) {
			t.Errorf("%q should be allowed", u)
		}
	}
}
