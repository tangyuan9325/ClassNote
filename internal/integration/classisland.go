// Package integration 负责与 ClassIsland 课表、InkCanvasForClass 的联动。
package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"classnote/internal/capture"
	"classnote/internal/config"
	"classnote/internal/logx"
)

// Lesson 是一节课。
type Lesson struct {
	Subject string    `json:"subject"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Index   int       `json:"index"`
	Room    string    `json:"room,omitempty"`
	Teacher string    `json:"teacher,omitempty"`
}

// ClassState 描述当前课程状态。
type ClassState struct {
	Running   bool      `json:"running"`
	Detected  bool      `json:"detected"`
	Plugin    bool      `json:"plugin,omitempty"`
	DataDir   string    `json:"dataDir"`
	Profile   string    `json:"profile"`
	InClass   bool      `json:"inClass"`
	Preparing bool      `json:"preparing"`
	Current   *Lesson   `json:"current,omitempty"`
	Next      *Lesson   `json:"next,omitempty"`
	Today     []Lesson  `json:"today"`
	Source    string    `json:"source"` // profile | manual | push | plugin | none
	Monitored bool      `json:"monitored"`
	UpdatedAt time.Time `json:"updatedAt"`
	Hint      string    `json:"hint,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ClassIslandProvider 读取 ClassIsland 的课表文件。
type ClassIslandProvider struct {
	mu       sync.RWMutex
	cfg      config.ClassIslandConfig
	subjects []string
	manual   []config.TimetableEntry
	pushed   *ClassState
	pushAt   time.Time
	state    ClassState
	lastLoad time.Time
	profile  *ciProfile
	profPath string
	loadedOK bool

	// 注入插件（ClassIsland 内运行）的状态缓存
	pluginClient *http.Client
	pluginLast   PluginState
	pluginAt     time.Time
	pluginOK     bool
	pluginPort   int
}

type ciProfile struct {
	Name        string                  `json:"Name"`
	TimeLayouts map[string]ciTimeLayout `json:"TimeLayouts"`
	ClassPlans  map[string]ciClassPlan  `json:"ClassPlans"`
	Subjects    map[string]ciSubject    `json:"Subjects"`
	Groups      map[string]ciGroup      `json:"ClassPlanGroups"`
	SelectedGrp string                  `json:"SelectedClassPlanGroupId"`
	OverlayOn   bool                    `json:"IsOverlayClassPlanEnabled"`
	OverlayID   *string                 `json:"OverlayClassPlanId"`
	TempID      *string                 `json:"TempClassPlanId"`
}

type ciTimeLayout struct {
	Name    string         `json:"Name"`
	Layouts []ciLayoutItem `json:"Layouts"`
}

type ciLayoutItem struct {
	StartTime string `json:"StartTime"`
	EndTime   string `json:"EndTime"`
	TimeType  int    `json:"TimeType"`
	BreakName string `json:"BreakName"`
}

type ciClassPlan struct {
	TimeLayoutID string `json:"TimeLayoutId"`
	TimeRule     struct {
		WeekDay           int `json:"WeekDay"`
		WeekCountDiv      int `json:"WeekCountDiv"`
		WeekCountDivTotal int `json:"WeekCountDivTotal"`
	} `json:"TimeRule"`
	Classes []struct {
		SubjectID string `json:"SubjectId"`
		IsEnabled bool   `json:"IsEnabled"`
	} `json:"Classes"`
	Name          string `json:"Name"`
	IsEnabled     bool   `json:"IsEnabled"`
	IsOverlay     bool   `json:"IsOverlay"`
	AssociatedGrp string `json:"AssociatedGroup"`
}

type ciSubject struct {
	Name        string `json:"Name"`
	Initial     string `json:"Initial"`
	TeacherName string `json:"TeacherName"`
}

type ciGroup struct {
	Name     string `json:"Name"`
	IsGlobal bool   `json:"IsGlobal"`
}

// NewClassIsland 创建课表联动。
func NewClassIsland(cfg config.ClassIslandConfig, subjects []string, manual []config.TimetableEntry) *ClassIslandProvider {
	return &ClassIslandProvider{
		cfg: cfg, subjects: subjects, manual: manual,
		pluginClient: &http.Client{Timeout: 400 * time.Millisecond},
	}
}

// Update 更新配置。
func (p *ClassIslandProvider) Update(cfg config.ClassIslandConfig, subjects []string, manual []config.TimetableEntry) {
	p.mu.Lock()
	p.cfg = cfg
	p.subjects = subjects
	p.manual = manual
	p.profile = nil
	p.loadedOK = false
	p.mu.Unlock()
}

// Push 接收外部（插件/脚本）推送的状态。
func (p *ClassIslandProvider) Push(st ClassState) {
	p.mu.Lock()
	st.Source = "push"
	st.UpdatedAt = time.Now()
	p.pushed = &st
	p.pushAt = time.Now()
	p.mu.Unlock()
}

// State 返回当前状态，必要时重新读取课表。
func (p *ClassIslandProvider) State() ClassState {
	p.mu.RLock()
	pushed, pushAt := p.pushed, p.pushAt
	cfg := p.cfg
	p.mu.RUnlock()

	now := time.Now()
	if pushed != nil && now.Sub(pushAt) < 90*time.Second {
		st := *pushed
		st.Detected = true
		st.Monitored = p.monitored(st.Current)
		return st
	}

	// 注入插件优先：能拿到实时状态时刷新缓存（失败不影响文件解析）。
	if cfg.Enabled {
		_, _ = p.probePlugin(false)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !cfg.Enabled {
		return ClassState{Source: "none", UpdatedAt: now, Error: "课表联动已关闭"}
	}
	// 课表文件变化或超过 30 秒重新加载。
	if p.profile == nil || now.Sub(p.lastLoad) > 30*time.Second || p.changedOnDisk() {
		p.loadProfile()
	}
	st := p.compute(now)
	p.state = st
	return st
}

func (p *ClassIslandProvider) changedOnDisk() bool {
	if p.profPath == "" {
		return false
	}
	fi, err := os.Stat(p.profPath)
	if err != nil {
		return false
	}
	return fi.ModTime().After(p.lastLoad)
}

// DataDir 返回解析出的 ClassIsland 数据目录。
func (p *ClassIslandProvider) DataDir() string {
	if p.cfg.DataDir != "" {
		if st, err := os.Stat(p.cfg.DataDir); err == nil && st.IsDir() {
			return p.cfg.DataDir
		}
	}
	if p.cfg.ExePath != "" {
		if st, err := os.Stat(p.cfg.ExePath); err == nil && !st.IsDir() {
			d := filepath.Join(filepath.Dir(p.cfg.ExePath), "data")
			if st2, err := os.Stat(d); err == nil && st2.IsDir() {
				return d
			}
		}
	}
	if dir := capture.FindProcessDir("ClassIsland.exe", "ClassIsland.Desktop.exe", "ClassIsland.Desktop"); dir != "" {
		// 便携版的 data 在启动器所在目录；Desktop.exe 在 app-x.y.z 子目录里，
		// 所以要同时检查自身目录和上一级目录。
		for _, base := range []string{dir, filepath.Dir(dir)} {
			d := filepath.Join(base, "data")
			if st, err := os.Stat(filepath.Join(d, "Profiles")); err == nil && st.IsDir() {
				return d
			}
		}
	}
	candidates := []string{}
	// 显式指定优先：便携版、绿色版、或者装在非标准目录时全靠它。
	if env := strings.TrimSpace(os.Getenv("CLASSISLAND_DATA_DIR")); env != "" {
		candidates = append(candidates, env)
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		candidates = append(candidates,
			filepath.Join(appData, "ClassIsland", "data"),
			filepath.Join(appData, "ClassIsland"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates,
			filepath.Join(local, "ClassIsland", "data"),
			filepath.Join(local, "ClassIsland"),
			// ClassIsland 2.x 把配置放在 Local 下的包目录里。
			filepath.Join(local, "ClassIsland.App", "data"))
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		candidates = append(candidates,
			filepath.Join(home, "ClassIsland", "data"),
			filepath.Join(home, "Documents", "ClassIsland", "data"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "data"),
			filepath.Join(filepath.Dir(wd), "data"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "Profiles")); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

func (p *ClassIslandProvider) loadProfile() {
	p.lastLoad = time.Now()
	dir := p.DataDir()
	if dir == "" {
		p.profile = nil
		p.loadedOK = false
		return
	}
	profDir := filepath.Join(dir, "Profiles")
	selected := ""
	if raw, err := os.ReadFile(filepath.Join(dir, "Settings.json")); err == nil {
		var s struct {
			SelectedProfile string `json:"SelectedProfile"`
		}
		if json.Unmarshal(stripBOM(raw), &s) == nil {
			selected = s.SelectedProfile
		}
	}
	path := ""
	if selected != "" {
		cand := filepath.Join(profDir, selected)
		if _, err := os.Stat(cand); err == nil {
			path = cand
		}
	}
	if path == "" {
		// 取最近修改的档案。
		entries, _ := os.ReadDir(profDir)
		var newest os.FileInfo
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if newest == nil || info.ModTime().After(newest.ModTime()) {
				newest = info
				path = filepath.Join(profDir, e.Name())
			}
		}
	}
	if path == "" {
		p.profile = nil
		p.loadedOK = false
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		p.profile = nil
		p.loadedOK = false
		return
	}
	var pf ciProfile
	if err := json.Unmarshal(stripBOM(raw), &pf); err != nil {
		logx.Warnf("classisland", "解析课表失败: %v", err)
		p.profile = nil
		p.loadedOK = false
		return
	}
	first := p.profile == nil
	p.profile = &pf
	p.profPath = path
	p.loadedOK = true
	if first {
		logx.Infof("classisland", "已加载课表 %s（%d 个科目）", filepath.Base(path), len(pf.Subjects))
	}
}

func (p *ClassIslandProvider) compute(now time.Time) ClassState {
	st := ClassState{UpdatedAt: now, Detected: true, Source: "profile"}
	if p, ok := capture.FindProcessInfo("ClassIsland.exe", "ClassIsland.Desktop.exe", "ClassIsland.Desktop"); ok {
		st.Running = true
		_ = p
	}
	dir := p.DataDir()
	st.DataDir = dir
	if p.profPath != "" {
		st.Profile = filepath.Base(p.profPath)
	}

	lessons := p.lessonsFor(now)
	if len(lessons) == 0 {
		// 回退到手动课表。
		lessons = p.manualLessons(now)
		if len(lessons) > 0 {
			st.Source = "manual"
		}
	}
	st.Today = lessons
	for i := range lessons {
		l := lessons[i]
		if !now.Before(l.Start) && now.Before(l.End) {
			st.InClass = true
			st.Current = &l
			break
		}
		if now.Before(l.Start) && l.Start.Sub(now) <= 10*time.Minute {
			cp := l
			st.Preparing = true
			st.Next = &cp
		}
	}
	if st.Current == nil {
		for i := range lessons {
			if lessons[i].Start.After(now) {
				l := lessons[i]
				st.Next = &l
				break
			}
		}
	}
	if st.Current == nil && !st.Preparing && len(lessons) == 0 {
		if p.loadedOK {
			// 档案读到了，只是今天没排课：这不是故障，不要报错。
			st.Hint = weekdayHint(now)
		} else {
			st.Error = "未读取到课表，可在设置中手动指定 ClassIsland 数据目录"
		}
	}
	if p.pluginOK && now.Sub(p.pluginAt) < 5*time.Second {
		ps := p.pluginLast
		st.Plugin = true
		st.Detected = true
		st.Running = true
		st.Source = "plugin"
		// 插件能拿到 ClassIsland 的实时状态（临时调课、提前下课），
		// 但它说「不在上课」时不能反过来把课表算出来的结论抹掉：
		// 插件刚启动、ClassIsland 正在切课的那几秒都会误报 false。
		// 所以只在插件说「在课」时采信，说「不在课」时保留课表结果。
		if ps.InClass && ps.Subject != "" {
			l := Lesson{Subject: ps.Subject}
			if t, ok := parseClock(now, ps.Start); ok {
				l.Start = t
			}
			if t, ok := parseClock(now, ps.End); ok {
				l.End = t
			}
			st.InClass = true
			st.Preparing = false
			st.Current = &l
			st.Next = nil
			st.Hint = ""
		} else if ps.Next != "" && st.Current == nil {
			l := Lesson{Subject: ps.Next}
			if t, ok := parseClock(now, ps.NextStart); ok {
				l.Start = t
			}
			st.Preparing = ps.Preparing
			st.Next = &l
			st.Hint = ""
		}
		if ps.ClassName != "" {
			// 班级名单独放，别覆盖课表档案名——两者含义不同。
			st.Hint = ps.ClassName
		}
	}
	st.Monitored = p.monitored(st.Current)
	return st
}

// weekdayHint 给出“今天没有课”的可读说明。
func weekdayHint(now time.Time) string {
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return names[int(now.Weekday())] + "没有排课"
}

// normSubject 归一化学科名：去空白、去括号注记、全角转半角。
//
// ClassIsland 里同一个学科可能出现「语文」「语文（阅读）」「语文 A」几种写法，
// 用户填的监听列表又往往是「语文」。原来的双向 strings.Contains 在
// 「语文」vs「语」这种短名上会误匹配，所以先归一化再分档比较。
func normSubject(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'Ａ' && r <= 'Ｚ':
			b.WriteRune(r - 0xFEE0)
		case r >= 'ａ' && r <= 'ｚ':
			b.WriteRune(r - 0xFEE0)
		case r == '（' || r == '(' || r == '【' || r == '[':
			// 括号之后是注记，整段丢掉。
			return b.String()
		case r == ' ' || r == '\u3000' || r == '	':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SubjectNames 返回 ClassIsland 课表里出现过的全部科目名。
// 设置页用它做多选，用户不必再手打学科名——打错一个字就永远监听不到课。
func (p *ClassIslandProvider) SubjectNames() []string {
	p.mu.RLock()
	pf := p.profile
	enabled := p.cfg.Enabled
	manual := append([]config.TimetableEntry(nil), p.manual...)
	p.mu.RUnlock()
	if !enabled {
		return nil
	}
	if pf == nil {
		p.mu.Lock()
		p.loadProfile()
		pf = p.profile
		p.mu.Unlock()
	}
	out := make([]string, 0, 16)
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		k := normSubject(name)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, name)
	}
	if pf != nil {
		ids := make([]string, 0, len(pf.Subjects))
		for id := range pf.Subjects {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			add(pf.Subjects[id].Name)
		}
	}
	for _, e := range manual {
		add(e.Subject)
	}
	// 插件实时上报的科目也带上：临时课表可能不在 profile 里。
	if st, ok := p.pluginSnapshot(); ok {
		add(st.Subject)
		add(st.Next)
	}
	sort.Strings(out)
	return out
}

// SubjectSource 说明科目列表是从哪来的。
func (p *ClassIslandProvider) SubjectSource() string {
	p.mu.RLock()
	enabled, ok, path, nman := p.cfg.Enabled, p.loadedOK, p.profPath, len(p.manual)
	p.mu.RUnlock()
	switch {
	case !enabled:
		return "off"
	case ok && path != "":
		return "classisland"
	case nman > 0:
		return "manual"
	}
	return "none"
}

func (p *ClassIslandProvider) monitored(l *Lesson) bool {
	if l == nil {
		return false
	}
	p.mu.RLock()
	subjects := append([]string(nil), p.subjects...)
	p.mu.RUnlock()
	if len(subjects) == 0 {
		return false
	}
	target := normSubject(l.Subject)
	if target == "" {
		return false
	}
	// 第一轮：归一化后完全相等，最可靠。
	for _, s := range subjects {
		if normSubject(s) == target {
			return true
		}
	}
	// 第二轮：一方包含另一方，但短的一方至少两个字，
	// 避免把「语」配到「英语」这类单字误判。
	for _, s := range subjects {
		n := normSubject(s)
		if len([]rune(n)) < 2 || len([]rune(target)) < 2 {
			continue
		}
		if strings.Contains(target, n) || strings.Contains(n, target) {
			return true
		}
	}
	return false
}

func (p *ClassIslandProvider) lessonsFor(now time.Time) []Lesson {
	if p.profile == nil {
		return nil
	}
	pf := p.profile
	weekday := int(now.Weekday()) // 0=Sunday，与 ClassIsland 一致
	if pf.TempID != nil && *pf.TempID != "" {
		if cp, ok := pf.ClassPlans[*pf.TempID]; ok {
			return p.expand(cp, now, weekday)
		}
	}
	if pf.OverlayOn && pf.OverlayID != nil && *pf.OverlayID != "" {
		if cp, ok := pf.ClassPlans[*pf.OverlayID]; ok {
			return p.expand(cp, now, weekday)
		}
	}
	// 挑选当天的启用课表。
	// 按 id 排序遍历：map 的随机序会让同分候选每天选到不同的课表。
	planIDs := make([]string, 0, len(pf.ClassPlans))
	for id := range pf.ClassPlans {
		planIDs = append(planIDs, id)
	}
	sort.Strings(planIDs)
	var best *ciClassPlan
	bestScore := -1
	for _, id := range planIDs {
		cp := pf.ClassPlans[id]
		if cp.IsOverlay || !cp.IsEnabled {
			continue
		}
		if cp.TimeRule.WeekDay != weekday {
			continue
		}
		// 单双周：1.2.5 解析了 WeekCountDiv 却从未使用，隔周上的课会被当成每周都上。
		if !weekRuleMatches(cp.TimeRule.WeekCountDiv, cp.TimeRule.WeekCountDivTotal, now) {
			continue
		}
		if pf.SelectedGrp != "" && cp.AssociatedGrp != "" && cp.AssociatedGrp != pf.SelectedGrp &&
			cp.AssociatedGrp != "00000000-0000-0000-0000-000000000000" {
			continue
		}
		score := 0
		if cp.AssociatedGrp == pf.SelectedGrp {
			score += 2
		}
		if score > bestScore {
			c := cp
			best = &c
			bestScore = score
		}
	}
	if best == nil {
		return nil
	}
	return p.expand(*best, now, weekday)
}

func (p *ClassIslandProvider) expand(cp ciClassPlan, now time.Time, weekday int) []Lesson {
	pf := p.profile
	tl, ok := pf.TimeLayouts[cp.TimeLayoutID]
	if !ok {
		return nil
	}
	var out []Lesson
	classIdx := 0
	period := 0
	for _, item := range tl.Layouts {
		if item.TimeType != 0 {
			continue
		}
		period++
		if classIdx >= len(cp.Classes) {
			break
		}
		cls := cp.Classes[classIdx]
		classIdx++
		if !cls.IsEnabled {
			continue
		}
		subj, ok := pf.Subjects[cls.SubjectID]
		if !ok || strings.TrimSpace(subj.Name) == "" {
			continue
		}
		start, ok1 := parseClock(now, item.StartTime)
		end, ok2 := parseClock(now, item.EndTime)
		if !ok1 || !ok2 {
			continue
		}
		if end.Before(start) || end.Equal(start) {
			continue
		}
		out = append(out, Lesson{
			Subject: subj.Name, Start: start, End: end, Index: period,
			Teacher: subj.TeacherName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (p *ClassIslandProvider) manualLessons(now time.Time) []Lesson {
	var out []Lesson
	for i, e := range p.manual {
		if e.Weekday != int(now.Weekday()) {
			continue
		}
		start, ok1 := parseClock(now, e.Start)
		end, ok2 := parseClock(now, e.End)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, Lesson{Subject: e.Subject, Start: start, End: end, Index: i + 1, Room: e.Room})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func parseClock(base time.Time, s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{"15:04:05", "15:04", "2006-01-02T15:04:05"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, base.Location()); err == nil {
			return time.Date(base.Year(), base.Month(), base.Day(), t.Hour(), t.Minute(), t.Second(), 0, base.Location()), true
		}
	}
	return time.Time{}, false
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

// Timetable 返回今日课程表。
func (p *ClassIslandProvider) Timetable() []Lesson {
	return p.State().Today
}

// Summary 生成一行状态描述。
func (p *ClassIslandProvider) Summary() string {
	st := p.State()
	switch {
	case st.InClass && st.Current != nil:
		return fmt.Sprintf("上课中 · %s（至 %s）", st.Current.Subject, st.Current.End.Format("15:04"))
	case st.Preparing && st.Next != nil:
		return fmt.Sprintf("即将上课 · %s %s", st.Next.Subject, st.Next.Start.Format("15:04"))
	case st.Next != nil:
		return fmt.Sprintf("下一节 · %s %s", st.Next.Subject, st.Next.Start.Format("15:04"))
	default:
		return "非上课时间"
	}
}

// weekRuleMatches 判断今天是否命中单双周/多周轮换规则。
//
// ClassIsland 的 TimeRule.WeekCountDiv：0 表示每周；1..Total 表示该规则只在
// 第 1/2/... 周生效（Total 默认 2，即单双周）。这里没有学期起始日可用，
// 用 ISO 周数近似——对单双周这种二元轮换足够准，多周轮换会有相位偏差。
func weekRuleMatches(div, total int, now time.Time) bool {
	if div <= 0 {
		return true
	}
	if total <= 0 {
		total = 2
	}
	_, week := now.ISOWeek()
	return div%total == week%total
}
