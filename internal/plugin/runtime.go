package plugin

import (
	"sort"
	"strings"
	"sync"
)

// SlotBinding 是「某个启用插件占了某个界面槽位」这一事实。
type SlotBinding struct {
	PluginID string `json:"pluginId"`
	Slot     string `json:"slot"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	Href     string `json:"href,omitempty"`
	Order    int    `json:"order,omitempty"`
}

// Snapshot 是插件仓库在当前时刻的只读投影。
//
// 参照 DSH 的做法：插件变更不是「改一个全局变量让各处自己去读」，
// 而是产出一份不可变快照，Agent / 提示词 / 界面各自取一份用。
// 这样即使有人在课上点「停用插件」，正在跑的那次分析也不会半路换提示词。
type Snapshot struct {
	Prompt     string        `json:"prompt"`
	Tools      []string      `json:"tools"`
	Slots      []SlotBinding `json:"slots"`
	Events     []string      `json:"events"`
	Grants     []Grant       `json:"grants"`
	Enabled    int           `json:"enabled"`
	Broken     []Broken      `json:"broken,omitempty"`
	Generation int           `json:"generation"`
}

// Grant 表示「某插件被授予了某权限」，供审计与界面展示。
type Grant struct {
	PluginID string   `json:"pluginId"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Perms    []string `json:"perms"`
	Tools    []string `json:"tools"`
}

var (
	snapMu  sync.Mutex
	snapGen = -1
	snapVal Snapshot
)

// Snapshot 返回当前快照（按 generation 缓存）。
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	gen := r.gen
	plugins := make([]Plugin, len(r.plugins))
	copy(plugins, r.plugins)
	broken := make([]Broken, len(r.broken))
	copy(broken, r.broken)
	r.mu.RUnlock()

	snapMu.Lock()
	defer snapMu.Unlock()
	if gen == snapGen && snapVal.Generation == gen {
		return snapVal
	}

	snap := Snapshot{Generation: gen, Broken: broken}
	var prompt strings.Builder
	promptN := 0
	toolSet := map[string]bool{}
	eventSet := map[string]bool{}
	for _, p := range plugins {
		if !p.Enabled || p.Kind == KindExternal {
			continue
		}
		snap.Enabled++
		snap.Grants = append(snap.Grants, Grant{
			PluginID: p.ID, Name: p.Name, Kind: p.Kind,
			Perms: append([]string(nil), p.Permissions...),
			Tools: append([]string(nil), p.Tools...),
		})
		if p.Has(CapPrompt) && strings.TrimSpace(p.Prompt) != "" {
			if promptN == 0 {
				prompt.WriteString("\n\n【已启用的插件】\n")
			}
			promptN++
			prompt.WriteString("- " + p.Name + "：" + p.Description + "\n  " + strings.TrimSpace(p.Prompt) + "\n")
		}
		if p.Has(CapTools) {
			for _, t := range p.Tools {
				toolSet[t] = true
			}
		}
		if p.Has(CapEvents) {
			for _, e := range p.Events {
				eventSet[e] = true
			}
		}
		if p.Has(CapUI) && p.UI != nil {
			snap.Slots = append(snap.Slots, SlotBinding{
				PluginID: p.ID, Slot: p.UI.Slot, Title: p.UI.Title,
				Body: p.UI.Body, Href: p.UI.Href, Order: p.UI.Order,
			})
		}
	}
	snap.Prompt = prompt.String()
	for t := range toolSet {
		snap.Tools = append(snap.Tools, t)
	}
	sort.Strings(snap.Tools)
	for e := range eventSet {
		snap.Events = append(snap.Events, e)
	}
	sort.Strings(snap.Events)
	sort.Slice(snap.Slots, func(i, j int) bool {
		if snap.Slots[i].Slot != snap.Slots[j].Slot {
			return snap.Slots[i].Slot < snap.Slots[j].Slot
		}
		if snap.Slots[i].Order != snap.Slots[j].Order {
			return snap.Slots[i].Order < snap.Slots[j].Order
		}
		return snap.Slots[i].PluginID < snap.Slots[j].PluginID
	})
	snapGen = gen
	snapVal = snap
	return snap
}

// Invalidate 在外部直接改写了插件文件后调用，强制下次取快照时重算。
func (r *Registry) Invalidate() {
	r.mu.Lock()
	r.gen++
	r.mu.Unlock()
}
