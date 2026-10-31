package app

import (
	"context"
	"fmt"
	"time"

	"classnote/internal/config"
	"classnote/internal/integration"
	"classnote/internal/lesson"
	"classnote/internal/logx"
)

// Monitor 轮询 ClassIsland 状态，自动开始/结束课堂。
type Monitor struct {
	engine      *lesson.Engine
	ci          *integration.ClassIslandProvider
	icc         *integration.ICCBridge
	exeDir      string
	cfg         func() config.Config
	wasIn       bool
	wasMon      bool
	autoStarted bool
	injectTried bool
	lastLog     time.Time
}

// NewMonitor 创建监听器。
func NewMonitor(engine *lesson.Engine, ci *integration.ClassIslandProvider, icc *integration.ICCBridge,
	exeDir string, cfg func() config.Config) *Monitor {
	return &Monitor{engine: engine, ci: ci, icc: icc, exeDir: exeDir, cfg: cfg}
}

// autoInject 在检测到 ICC 运行时自动安装桥接插件（只尝试一次）。
func (m *Monitor) autoInject(cfg config.Config) {
	if m.injectTried || m.icc == nil || !cfg.Integ.ICC.Enabled || !cfg.Integ.ICC.AutoInject {
		return
	}
	st := m.icc.State()
	if st.PluginInstalled {
		return
	}
	// ICC 运行时它的配置与插件目录都被占用，改动会在退出时被覆盖，
	// 因此在 ICC 未运行时才自动注入；运行中的情况由界面提示用户。
	if st.Running {
		return
	}
	if st.InstallDir == "" {
		return
	}
	src := integration.FindLocalPlugin(m.exeDir)
	if src == "" {
		return
	}
	m.injectTried = true
	if _, err := m.icc.CopyPlugin(src); err != nil {
		logx.Warnf("monitor", "自动注入 ICC 插件失败: %v", err)
		m.engine.EmitExternal("error", map[string]string{"scope": "icc", "message": "自动注入插件失败: " + err.Error()})
		return
	}
	logx.Infof("monitor", "已自动注入 ICC 桥接插件")
	m.engine.EmitExternal("icc_injected", map[string]any{"source": src})
}

// Run 阻塞运行，直到 ctx 结束。
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *Monitor) tick() {
	cfg := m.cfg()
	m.autoInject(cfg)
	state := m.ci.State()
	st := m.engine.Status()

	// 事件通知（状态变化时）。
	if state.InClass != m.wasIn {
		m.wasIn = state.InClass
		m.engine.EmitExternal("class_state", map[string]any{
			"inClass": state.InClass, "subject": subject(state.Current),
			"monitored": state.Monitored, "source": state.Source,
		})
	}
	m.wasMon = state.Monitored

	if !st.Running {
		m.autoStarted = false
	}

	// 自动开始：仅在监听的学科上课时。
	if cfg.Lesson.AutoStart && state.InClass && state.Monitored && !st.Running {
		subjectName := "课堂"
		title := ""
		if state.Current != nil {
			subjectName = state.Current.Subject
			title = fmt.Sprintf("%s · %s", subjectName, state.Current.Start.Format("01-02 15:04"))
		}
		if err := m.engine.Start(subjectName, title, "classisland"); err != nil {
			logx.Warnf("monitor", "自动开始失败: %v", err)
		} else {
			m.autoStarted = true
			logx.Infof("monitor", "检测到上课（%s），已自动开始记录", subjectName)
			m.engine.EmitExternal("auto_start", map[string]string{"subject": subjectName})
		}
		return
	}

	// 自动结束：只结束由课表自动开启的课堂，避免误停手动记录。
	if cfg.Lesson.AutoStop && st.Running && m.autoStarted && !state.InClass {
		if time.Since(st.StartedAt) < 45*time.Second {
			return
		}
		m.autoStarted = false
		go func() {
			if cfg.Lesson.AutoReport {
				if err := m.engine.StopAndReport(); err != nil {
					logx.Warnf("monitor", "自动结束失败: %v", err)
				}
			} else {
				if err := m.engine.Stop(); err != nil {
					logx.Warnf("monitor", "自动结束失败: %v", err)
				}
			}
			m.engine.EmitExternal("auto_stop", map[string]any{"at": time.Now()})
		}()
	}
}

func subject(l *integration.Lesson) string {
	if l == nil {
		return ""
	}
	return l.Subject
}
