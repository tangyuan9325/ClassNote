# ClassNote · 课记

> 一个跑在 Windows 上的本地课堂助手：自动跟随幻灯片翻页、实时转写讲解、逐页生成 AI 笔记，并在下课时交出一份图文并茂的课堂报告（HTML + PDF）。

ClassNote 面向真实的教学环境设计：它不要求教师改变上课习惯，也不需要把课堂内容上传到某个云端平台。程序以 Go 编写，单文件运行，界面是本地网页，数据全部保存在程序所在目录。

**当前版本 v1.8.1** —— **语音模型库**：五种 whisper 模型（Tiny 32 MB → Large v3 Turbo 574 MB）在设置页一键下载，带进度、可续传、可切换、可删除；发布包内直接带一个 60 MB 的轻量模型，装好就能用；没有模型时首次启动自动下载。另外补齐了识别语言选择与下载源选择。详见[更新日志](#更新日志)。

![主界面](docs/images/main.png)

---

## 目录

- [它能做什么](#它能做什么)
- [30 秒上手](#30-秒上手)
- [工作原理](#工作原理)
- [课堂 Agent](#课堂-agent)
- [插件（v2）与插件市场](#插件v2与插件市场)
- [内置 DSH 运行时](#内置-dsh-运行时)
- [界面样式：经典 / DSH](#界面样式经典--dsh)
- [配置说明](#配置说明)
- [语音与模型](#语音模型)
- [与 ClassIsland / InkCanvasForClass 联动](#与-classisland--inkcanvasforclass-联动)
- [数据目录](#数据目录)
- [构建](#构建)
- [已知限制](#已知限制)
- [常见问题](#常见问题)
- [更新日志](#更新日志)
- [开源许可](#开源许可)

---

## 它能做什么

| 能力 | 说明 |
| --- | --- |
| **自动跟页** | 检测 PowerPoint / WPS 演示的放映状态，翻页瞬间抓取当前页。优先通过 COM 导出高清原图（1920×1080、无遮挡、无批注），失败时回退到窗口截图 + 感知哈希比对 |
| **随堂转写** | 麦克风采集 → 能量检测断句 → 语音转写接口（或本地 whisper.cpp），按页归档 |
| **逐页 AI 笔记** | 每页停留结束时，把「画面文字 + 录音转写 + 上一页要点」交给模型，输出结构化笔记、要点、易错点 |
| **照搬判定** | 判断教师是否在逐字念 PPT，给出 0–100 的照搬分与证据；没有语音证据时明确标为「未判定」，不瞎猜 |
| **课堂报告** | 下课后汇总为 HTML 报告，先给讲授方式判断，再是逐页笔记与实录摘录，最后用浏览器内核渲染成 PDF |
| **课表联动** | 直接解析 ClassIsland 的课表文件，判断上下课与学科；只监听你勾选的学科（默认语文） |
| **ClassIsland 注入插件** | 随包提供 ClassIsland 插件（`classnote.islandbridge`）：上课/下课、当前科目、下一节课实时注入，还能让 ClassIsland 弹出课堂提醒 |
| **ICC 插件** | 随包提供 `ClassNote.Bridge.dll`，注入后 ICC 的放映状态实时推送到 ClassNote |
| **系统托盘** | 原生托盘图标：右键开始/结束记录、打开报告目录、状态气泡；图标丢失自动重挂 |
| **本地语音转写** | whisper.cpp 随包分发（含 Alder Lake / AVX2 等 CPU 后端），首次启动自动释放到 `C:\ProgramData`，完全离线；4.6 秒中文语音约 3 秒出结果 |
| **语音模型库** | 5 种模型一键下载（Tiny / Base / Small / Medium / Large v3 Turbo），多源断点续传 + SHA-256 校验 + 实时进度；可随时切换、删除；发布包自带 60 MB 轻量模型，无网也能用 |
| **录音批次限制** | 「每批最长（秒）」到达上限时回溯到最近一次句间停顿，把最后一句留到下一批，当前批次立即送转写 |
| **桌面悬浮球** | 常驻屏幕右上角、可拖动，显示课堂状态与计时，悬停展开，点开主界面 |
| **左侧导航栏** | 仿 ClassIsland 的左侧竖栏，可展开/收拢；主页 / Agent / 插件商城 / 主题商店 |
| **局域网访问** | 手机/平板用带密钥的链接访问；密钥随机生成、可改可持久化，非回环请求强制校验 |
| **机器人通知** | 企业微信 / 钉钉 / 飞书 / Telegram / Server酱 / Discord / Slack / Bark / PushPlus / ntfy / Gotify / 自定义 Webhook，快速添加可搜索 |
| **主题商店** | 4 套内置 + 自定义取色编辑（实时预览）+ 导出导入 + 社区主题一键安装 |
| **内置 DSH 运行时** | 插件页顶部一键装 Node + DeepSeek Harness（约 600 MB，只需一次），装完即可在 ClassNote 里安装、启停、运行 DSH 插件，DSH 的界面直接嵌在同一页 |
| **插件市场（双渠道）** | 「ClassNote」渠道装本地插件，装完即用；「DSH」渠道收录 DeepSeek Harness 的 4104 个插件，装了运行时就能一键真装，没装则给引导与命令 |
| **插件（v2 声明式）** | 插件不再只是一段提示词：可声明身份、类型（提示词 / 工具 / 界面 / 外部引用）、能力面、最小权限、界面槽位与事件订阅，由宿主解释执行，**不加载任何第三方可执行代码** |
| **课堂 Agent** | 真的会调用工具的多步 Agent：读课、翻页检索、写笔记、生成报告、查课表。模型列表自带**网页版 Qwen / DeepSeek**（免 API Key），也可走任意 OpenAI 兼容接口 |
| **界面样式切换** | 设置 → 外观里一键切换「经典」（1.2.6 及以前的布局）与「DSH」（借鉴 DeepSeek Harness 的会话栏 + 浮起输入区布局），随时可切回 |
| **Agent 页** | 独立聊天页：课堂上下文模式 + 通用模式，快捷问题，按课保存对话 |
| **Agent 对话** | 主页随时向 Agent 提问，对话记录按课保存；历史课堂下可展开每节课的对话记录 |
| **权限闸门** | 权限三档：全部访问 / 敏感提醒 / 人工审查；写笔记与生成报告默认要先批准，审批凭证随机生成、一次性有效 |
| **一键更新** | 关于页检查新版本后直接下载更新包、安装并重启，全程不离开应用 |
| **双端 UI** | 同一套界面既能在本地应用窗口打开，也能用浏览器访问；四套主题（羊皮纸 / 石墨 / 雾蓝 / 松林） |

![环境检查](docs/images/env.png)

---

## 30 秒上手

### 直接运行

1. 下载 Release 中的 `ClassNote-<版本>-windows-amd64.zip`，解压到任意目录（不要放在 `C:\Program Files` 下，避免写权限问题）。
2. 双击 `ClassNote.exe`。首次启动会弹出引导页：
   - **欢迎** → 了解它能做什么；
   - **模型** → 选一个 API 服务商填入 Key，或切到「网页版 AI」用浏览器登录；
   - **环境** → 自动检测 PowerPoint / WPS、麦克风、ClassIsland、InkCanvasForClass。
3. 进入主界面。上课时点击左侧「开始记录」，或者让 ClassIsland 自动触发。

> 程序会在 `127.0.0.1:41735` 起一个本地服务，并用 Edge 的应用模式打开窗口。想用浏览器访问，直接打开 `http://127.0.0.1:41735` 即可。

### 命令行参数

```text
ClassNote.exe [参数]

  -port int        本地服务起始端口（默认 41735）
  -no-window       只启动服务，不打开应用窗口
  -browser         用默认浏览器打开界面
  -v               日志同时输出到控制台
  -reset           重置配置（保留课堂记录）
```

---

## 工作原理

```text
                 ┌──────────────────────────────┐
   ClassIsland ──┤ 课表解析（上下课 / 学科）      │
                 └──────────────┬───────────────┘
                                │ 上课 / 下课
                 ┌──────────────▼───────────────┐
   PowerPoint ───┤ 放映检测（COM / 窗口 / 哈希） │
   WPS 演示   ───┤ 翻页 → 截图 + PPTX 文字      │
                 └──────────────┬───────────────┘
                                │ 每页材料
   麦克风 ────────► 断句 ──► ASR ─┤
                                │
                 ┌──────────────▼───────────────┐
                 │ 逐页分析（AI）                │
                 │ 笔记 / 要点 / 照搬评分        │
                 └──────────────┬───────────────┘
                                │ 下课后汇总
                 ┌──────────────▼───────────────┐
                 │ 报告 HTML → CDP 打印 PDF      │
                 └──────────────────────────────┘
```

几个关键设计：

1. **翻页是分段的唯一依据。** 每次翻页会立刻结算上一页：把残留音频送去转写、把这一页的转写归档、然后才开始新一页。这样「这一页讲了什么」不会串页。
2. **画面文字优先从 PPTX 里读。** `.pptx` 本质是 zip + XML，直接解析 `ppt/slides/slideN.xml` 里的文本节点，比截图 OCR 准，也不需要额外依赖；文件读不到时再依赖模型看图。
3. **COM 优先，截图兜底。** PowerPoint / WPS 的 COM 接口能拿到精确页码并导出干净的原图；接口不可用或被占用时，退回到 `PrintWindow`（能穿透其它置顶窗口）再退到屏幕 BitBlt。
4. **单进程、无 cgo。** 屏幕捕获、录音、COM、浏览器自动化全部走系统 API 或纯 Go 实现，`go build` 即可产出单文件。

---

## 课堂 Agent

1.2.7 把「跟你聊两句」的 Agent 换成了真的会动手的那种：**一步只做一件事，做完看结果，再决定下一步**。

### 它怎么跑

```text
问题 ──► 模型（只输出一个 JSON 动作）
          │
          ├─ {"thought":"…","tool":"lesson.search","args":{"q":"荷塘"}}
          │        │
          │        ├─ 权限闸门：这个工具要写笔记吗？要就发审批卡，老师点「允许」才执行
          │        ├─ 执行工具（45 秒超时，panic 会被兜住）
          │        └─ 把结果压成 2400 字观察，喂回模型，进入下一步
          │
          └─ {"thought":"…","final":"给老师看的回答"} ──► 结束
```

- **JSON 动作协议**：模型每步只回 `{"thought","tool","args"}` 或 `{"thought","final"}`。这样连没有 function calling 的网页版模型也能当 Agent 用（本项目要自带网页版 Qwen / DeepSeek，就是为了免 API Key 也能跑）。
- **步数上限**：默认 6 步，可设 1–12。用尽后不再无限绕圈，直接要求模型给出当前结论。
- **重复调用检测**：同一步的工具 + 参数如果和之前完全一样，判定为空转，中止并回一句说明，避免「反复读同一页」烧额度。
- **单飞**：同一时刻只跑一个 turn。老师连点发送键不会同时烧两份额度，也不会让两个写操作在课堂记录上互相覆盖。
- **会话作用域由宿主决定**：模型不能自己指定 `sessionId`。它说「读三年二班的记录」也只能读到当前这堂课 —— 这条有专门的单元测试盯着（`TestAgentIgnoresModelSessionID`）。

### 内置工具

| 工具 | 能力 | 性质 |
| --- | --- | --- |
| `lesson.overview` | 这堂课的整体情况：页数、时长、字数、报告状态 | 只读 |
| `lesson.read_page` | 读某一页的画面文字、转写、已有笔记 | 只读 |
| `lesson.search` | 在全课转写与笔记里搜关键词 | 只读 |
| `notes.append` | 给某一页追加笔记 | **写入，需审批** |
| `notes.update` | 重写某一页的笔记与小结 | **写入，需审批** |
| `report.generate` | 生成课堂报告（HTML + PDF） | **写入，需审批** |
| `timetable.today` | 今日课表与当前节次 | 只读 |
| `plugin.list` | 当前装了哪些插件、各自能用什么 | 只读 |

Agent 拿不到文件系统，也拿不到命令行 —— 这不是「暂时没做」，是刻意不给。`Host` 接口只暴露这 8 件事。

### 模型

设置 → Agent 里的模型列表固定前三项：

1. **跟随全局设置**
2. **网页版 Qwen**（`web:qwen`，免 API Key，走已登录的浏览器会话）
3. **网页版 DeepSeek**（`web:deepseek`，同上）

后面才是你配置的 `api:<模型名>`。网页版模式复用固定的浏览器 profile，登录一次就一直可用。

---

## 插件（v2）与插件市场

### 「插件」在 1.2.7 里的定义

1.2.6 及以前，所谓插件其实只是一个 JSON，里面一段 prompt 会被拼进系统提示词 —— 那更接近**技能包**。1.2.7 把它升级成有身份、有边界、有权限的插件清单（`schemaVersion: 2`）：

```json
{
  "schemaVersion": 2,
  "id": "note-assistant",
  "name": "随堂笔记助手",
  "version": "1.2.0",
  "kind": "skill",
  "capabilities": ["prompt", "tools"],
  "permissions": ["read:lesson", "write:report"],
  "tools": ["lesson.read_page", "notes.append"],
  "ui": { "slot": "statusbar", "title": "笔记助手" },
  "events": ["lesson.start", "slide.change"]
}
```

- **kind**：`skill`（提示词）/ `tool`（给 Agent 加工具）/ `ui`（占界面槽位）/ `external`（DSH 插件的引用，不启用、不注入提示词）。
- **capabilities**：这个插件**想用**哪些能力面（prompt / tools / ui / events）。
- **permissions**：它**声明**需要哪些最小权限（读课、写笔记、读报告、写报告、读课表、调模型、占界面）。`Normalize()` 会按 kind 与 capabilities 推导出最小集合，不会默认给全量。
- **ui.slot**：只能填 `statusbar` / `dashboard` / `settings` / `agent` 四个固定槽位 —— 插件不能自己往页面里塞任意 DOM。
- **外部链接只允许 https**，`kind: external` 必须有来源地址。

关键取舍：**插件里没有一行可执行代码**。DSH 的插件是真正的 npm 包，装进来就在宿主进程里跑；ClassNote 面向的是老师的办公电脑，所以只让插件**声明**、由宿主**解释**。装一个插件最多改到提示词、工具白名单和一个固定槽位，改不到文件系统。

> 迁移：老的 v1 插件文件不用动。`reload()` 会原样读入、自动补成 v2 并落盘，`Skill = Plugin` 的类型别名也保留着。

### 市场双渠道

「发现」页顶部有两个渠道：

| 渠道 | 内容 | 安装后 |
| --- | --- | --- |
| **ClassNote** | 本项目的插件清单（内置 + 仓库 `skills.json`） | 立刻可用，装完即生效 |
| **DSH** | [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) 的插件索引：**4104 个插件、23 个分类** | 装了运行时：按钮是「安装到 DSH」，**真的执行 npm 安装并重启运行时**；没装：引导去顶部装运行时，并保留「复制命令」给已经装了 DSH 的人 |

- DSH 渠道的索引是**内置快照**（`internal/plugin/dsh-plugins.json.gz`，578 KB），来自 npm 包 `dsh-plugin-catalog`（format 1），随程序分发，断网也能浏览；后台每 24 小时才试着更新一次。
- ClassNote 渠道走「缓存 + 后台刷新」（10 分钟 TTL），接口**从不等待网络**。之前每敲一个字就同步打一次 GitHub，断网时每次卡十几秒，属于会直接影响上课的 bug，已修。
- 索引地址只接受 https，且会拒绝回环 / 私网 / 链路本地地址，避免有人用一条 query 让本机去请求内网。

---

## 内置 DSH 运行时

### 为什么要有这一层

DSH 的插件是 **npm 包**，只有 DSH 自己能加载。1.2.7 里「DSH 渠道」只能帮你复制一条安装命令——那不叫能用，那叫给了个说明书。1.2.8 把这一步补上了：ClassNote 在本机准备一个 DSH 运行时，装插件、起进程、把界面嵌进插件页，全在同一个窗口里完成。

### 它怎么工作

```text
插件页顶部
  └─ 安装 DSH 运行时
       ├─ 没有 Node？自动下官方 Node 便携版（约 35 MB，解压即用，不写系统 PATH）
       ├─ corepack 生成 pnpm（装插件要用，不污染系统环境）
       ├─ npm install @deepseek-ai/dsh（约 600 MB，608 个包，实测 1 分 53 秒）
       └─ 写一份 MIT 许可到 data/dsh/runtime/，然后启动
            └─ dsh web --no-open --port <自动选端口>
                 └─ 从 stdout 抓登录令牌 → 拿到带 token 的地址
                      ├─ 「在页面内打开」：iframe 直接嵌进来
                      └─ 「新窗口打开」：交给系统浏览器
```

「发现」页里 DSH 的每个插件，按钮就是 **安装到 DSH**：真的跑 `dsh plugin --profile web add <包名>`，装完同步重启运行时让插件生效。

### 三条硬约束（都是实测出来的）

| 约束 | 说明 |
| --- | --- |
| **Node ≥ 22.19** | DSH 的入口用了 `import.meta.main`。低版本 Node 读不懂这个属性，CLI 会**静默退出、exit code 0、零输出**——看着像「插件坏了」，其实是运行时太旧。ClassNote 会先探测版本，不够就直接告诉你，并且能自动下 Node 24 |
| **运行时约 600 MB** | 608 个包，压不下去（可选依赖只省 60 MB，删 sourcemap 也只省 87 MB）。所以只能按需安装，不能塞进安装包 |
| **装插件要 pnpm** | `dsh plugin` 内部转发 pnpm。ClassNote 用 Node 自带的 corepack 生成 pnpm，装在 `data/dsh/node` 下，不动系统环境 |

### 版本不兼容怎么办

DSH 有版本守卫。插件要求的 DSH 版本与当前运行时对不上时，会**拒绝安装**并给出诊断。ClassNote 不会把这段英文原文甩给你，而是：

1. 识别出来，说明「这个插件要求 X，当前是 Y」；
2. 弹一句确认：「仍然安装可能崩溃或丢数据」；
3. 你确认后，它才用 `dsh plugin allow-version ... --accept-risk` 开豁免并重试。

### 数据与边界

```text
data/dsh/
  node/                 # 便携版 Node（含 npm、corepack 生成的 pnpm）
  runtime/              # DSH 运行时（含 LICENSE-DeepSeek-Harness.txt）
  home/profiles/web/    # DSH 的 web profile：package.json 里就是已装插件
  downloads/            # 下载缓存，装完即删
```

- **插件跑在 DSH 的进程和页面里，不跑在 ClassNote 里。** iframe 是跨源的（127.0.0.1 不同端口），插件碰不到 ClassNote 的 DOM、数据与课堂文件。这是刻意划的边界，不是没做。
- **ClassNote 不分发 DSH 的代码。** 运行时是从 npm 现场装的，装完在本地留一份 MIT 许可原文（`data/dsh/runtime/LICENSE-DeepSeek-Harness.txt`）与来源说明，满足 MIT 的署名义务。
- **退出 ClassNote 会关掉 DSH。** 不留孤儿进程占端口（这条是实测出来的：早先的实现在停止时会卡住，因为子进程没独立进程组）。

### 不想要这 600 MB？

可以完全不装。不装的时候：
- ClassNote 渠道的插件照常可用（它们由 ClassNote 自己解释执行，不需要 Node）；
- DSH 渠道只做索引浏览，给命令与仓库链接，不影响任何其它功能；
- 整个应用体积仍是 20 MB 出头。

---

## 界面样式：经典 / DSH

设置 → 外观 → 「界面样式」两张卡片，随时切换：

| 样式 | 说明 |
| --- | --- |
| **经典** | 1.2.6 及以前的左侧竖栏布局，功能位置一个没动 |
| **DSH** | 借鉴 DeepSeek Harness 的布局：左侧 268px 会话栏（最近课堂 + 启动按钮 + 剩余额度），顶栏 52px 发丝线分隔，内容区圆角卡片，底部浮起的胶囊输入区 |

DSH 样式只改 `document.documentElement.dataset.ui`，所有功能代码两套布局共用，不存在「切了样式功能就坏」的分支。

**来源与致谢**：DSH 样式在**布局结构与视觉规范**上借鉴 [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)（MIT License），包括栅格比例、52px 顶栏、R4/8/12/16/20/28 的圆角阶梯、0.5px 发丝线边框与浮起面板的层级规范。CSS 由本项目按上述规范自行实现，**未复制其源代码**；配色仍走 ClassNote 自己的主题变量，所以四套主题在两种样式下都成立。页面上也标注了来源（可在设置里关掉这条标注）。

---

## 配置说明

所有配置保存在程序目录的 `config.json`，也可以在界面「设置」里改。

### 模型

- **API 接入**：任何 OpenAI 兼容接口（DeepSeek、通义千问、Kimi、智谱、SiliconFlow、OpenAI、Ollama、中转站）以及 Anthropic 原生协议。
- **视觉**：勾选「模型支持图片理解」后，每页会连同截图一起发给模型；不勾选则只发文字。
- **网页版 AI**：用浏览器自动化操作 chat.qwen.ai / chat.deepseek.com。首次需在设置里点「打开登录」完成一次登录，登录态保存在 `data/webprofile`。网页版没有真正的 API，速度取决于页面响应（实测单页约 20–40 秒）。

> 想要最好的效果，建议用支持视觉的 API 模型；网页版适合没有 Key 或想先用起来的情况。

### 语音

| 模式 | 说明 |
| --- | --- |
| `local` | 本地 whisper.cpp，**默认**。模型在设置页里选，见下一节 |
| `api` | OpenAI 兼容的 `/audio/transcriptions`。SiliconFlow 的 `FunAudioLLM/SenseVoiceSmall` 免费且中文效果好 |
| `off` | 不转写，只记幻灯片 |

**识别语言**可选中文 / 英语 / 日语 / 自动检测。早先这里写死成 `-l zh`，英语课上会被强行转成汉字，v1.8.1 修掉了。

阈值越低越灵敏；教室噪声大时把「语音阈值」调高一点。没有麦克风时仍可正常记录幻灯片，也可以用「补录」按钮手动输入转写。

### 语音模型

设置 → 语音 → 本地语音，是一个模型列表。每个模型都标了体积、中文准确率、相对速度：

| 模型 | 体积 | 中文 | 速度 | 适用 |
| --- | --- | --- | --- | --- |
| Tiny | 32 MB | ★ | ★★★★★ | 很老的机器兜底，只能听个大概 |
| **Base** | **60 MB** | ★★ | ★★★★ | **默认，发布包自带**。日常讲课能出可读句子 |
| Small | 190 MB | ★★★ | ★★★ | 多数教室的最优解，8 代 i5 以上能实时跟上 |
| Medium | 539 MB | ★★★★ | ★★ | 术语更稳，低功耗本上会滞后几秒 |
| Large v3 Turbo | 574 MB | ★★★★★ | ★★★ | whisper 系列中文最好，有独显或新 CPU 时首选 |

- **下载**：点「下载」就在后台跑，界面显示百分比、速度、剩余时间，可随时取消；取消后保留已下部分，下次点「继续下载」接着来。
- **多源**：默认先走 `hf-mirror.com`（国内快），失败自动回落 `huggingface.co`，也可在页面里手动指定。
- **校验**：下完比对官方 SHA-256，不一致自动删掉重下一次，两次不行才报错——不会把半截文件当模型用。
- **切换**：点「使用」即时切换，whisper 服务重启并加载新模型，不用重启程序。
- **删除**：不用的模型可以删掉释放空间；正在用的那个会拒绝删除（否则服务重启后找不到模型）。

模型来自 [whisper.cpp 官方仓库](https://huggingface.co/ggerganov/whisper.cpp)（MIT）。发布包内已带 `ggml-base-q5_1.bin`，所以**断网、内网、没有镜像**的机器也能开箱转写；想要更准的再按需下载。

如果发布包里也没有模型（便携裁剪版），程序首次启动会在后台自动下载 Base，不阻塞启动，进度同步到界面。

### 截图

- **轮询间隔**：默认 700ms。COM 可用时开销很小。
- **高清导出**：优先用 COM 导出 1920×1080；关掉后只用窗口截图。
- **裁掉黑边**：自动去掉放映时的上下/左右黑边。
- **差异阈值**：窗口截图模式下判断「画面变了没有」，越大越迟钝。

### 课堂

- **监听学科**：默认只有「语文」。只有这些学科的课表时段才会自动开始记录。
- **自动开始 / 结束 / 生成报告**：自动结束只作用于「由课表自动开始」的课堂，手动开始的不会被误停。
- **备用课表**：ClassIsland 没开时，可以按「周一 08:00-08:40 语文」的格式手写。

---

## 与 ClassIsland / InkCanvasForClass 联动

### ClassIsland

ClassIsland 2.x 的课表是 JSON：`data/Profiles/*.json` 里有 `TimeLayouts`（时间轴）、`ClassPlans`（周几上什么课）、`Subjects`（科目）。ClassNote 直接读这些文件，按当前时间算出「现在是不是在上课、上什么课、下一节是什么」，因此 **ClassIsland 不开也能用**，只要数据目录在。

定位顺序：设置里的路径 → 运行中进程的安装目录 → `%APPDATA%\ClassIsland` → 手动指定。

### ClassIsland 注入插件（推荐）

读文件有 30 秒缓存，遇到临时换课、提前下课会有延迟。装上限定的注入插件后，ClassIsland 会把真实状态直接推过来：

- 插件 id：`classnote.islandbridge`，入口 `ClassNote.IslandBridge.dll`，目标 API 2.0。
- 安装：设置 → 集成 → 「安装 / 更新插件」，写入 `data/Plugins/classnote.islandbridge/`，重启 ClassIsland 生效。
- 接口：`GET http://127.0.0.1:51888/state` 返回当前科目、起止时间、下一节课、时间状态；`POST /notify` 让 ClassIsland 弹出提醒。
- 插件只在回环地址监听，不对外网开放；ClassNote 每 2 秒拉取一次，并与文件解析结果合并（插件优先）。
- 源码在 `integrations/classisland-bridge/`，可用 `dotnet build -c Release` 重新编译。

### InkCanvasForClass CE

ICC 的主窗口本身就是一层全屏透明覆盖层，所以「窗口是不是全屏」不能用来判断是否在放映。ClassNote 的做法是：

- **放映检测不依赖 ICC**：直接问 PowerPoint / WPS 的 COM 接口，或看放映窗口类名（`screenClass` 等），ICC 开着也不会干扰。
- **截图不被批注遮挡**：优先走 COM 导出原图；走窗口截图时用 `PrintWindow`，拿到的是窗口自身内容而不是屏幕像素。
- **事件桥（可选）**：任何外部程序都可以把 ICC 的状态推给 ClassNote：

```http
POST http://127.0.0.1:41735/api/integrations/icc/event
Content-Type: application/json

{ "event": "slideshow_start", "presenting": true, "slide": 3 }
```

**插件是随包提供的**（`plugins/ClassNote.Bridge.dll`，.NET Framework 4.7.2，对应 ICC 1.7.x）。
点「注入插件」会做三件事：复制 DLL 到 `<ICC>/Plugins/`、在 `Configs/PluginConfig.json` 里登记并启用、
写一份卸载说明。注入后重启 ICC 即可生效，移除时反向操作。

插件只做一件事：每秒读一次 ICC 的放映状态与页码，变化时（以及每 20 秒心跳）POST 一个事件过来。
实测在 ICC 1.7.18.8 上，插件自带的窗口探测比 `IGetService.IsPPTMode` 更可靠——
后者在这台机器上即使已经连上放映仍返回 `false`，所以插件以窗口特征为准。

需要为你自己的 ICC 版本重新编译时：

```powershell
cd integrations\icc-bridge
.\build.ps1 -IccDir "E:\...\InkCanvasForClass.CE.x.y.z"
```

---

## 数据目录

```text
ClassNote.exe
config.json                 # 配置（含 API Key，注意不要提交到仓库）
data/
  sessions/<id>.json        # 每堂课的结构化记录
  captures/<id>/*.jpg       # 逐页截图
  reports/<id>.html|.pdf    # 课堂报告
  logs/app-YYYYMMDD.log     # 运行日志
  plugins/skills/<id>.json  # 插件（v2 声明式清单，自动从 v1 迁移）
  cache/dsh-plugins.json    # DSH 插件索引的离线缓存（24h TTL）
  dsh/                      # 可选：DSH 运行时（Node + DeepSeek Harness + profile）
  webprofile/               # 网页版 AI 的浏览器登录态
  pdfprofile/               # 打印 PDF 用的独立浏览器配置
  runtime.json              # 端口与进程信息，供外部脚本对接
```

---

### 自己编译时的语音模型

仓库里**不放模型二进制**（60 MB 起，撑大仓库且每次 clone 都要重下）。发布包才带。
从源码构建时，提前下一份放进 `integrations/whisper/models/`：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\get-whisper.ps1
```

默认 `base-q5_1`（60 MB），脚本会顺带校验 SHA-256。也可以 `-Model small-q5_1`
或 `-Model large-v3-turbo-q5_0`。

不预先放也行：程序启动会自己下（见上一节）。这条脚本的用处是
「在离线机器或 CI 上预置好再打包」。

## 构建

需要 Go 1.22+（Windows）。项目没有 cgo 依赖。

```powershell
# 开发构建（带控制台，方便看日志）
go build -o ClassNote-debug.exe .

# 发布构建（无控制台窗口）
go build -ldflags "-H windowsgui -s -w -X main.version=1.0.0" -o ClassNote.exe .

# 打包
powershell -ExecutionPolicy Bypass -File build.ps1
```

`build.ps1` 会依次完成：编译 GUI 版与调试版 → 复制文档 → 生成 zip 到 `dist/`。

---

## 已知限制

这些是实际测试中确认的边界，写在这里避免误导：

1. **网页版 AI 不能可靠上传图片。** chat.qwen.ai 的文件输入框在 CDP 下能触发 `change` 事件，但页面不会生成附件预览，实测无法稳定把截图送进对话。因此网页版模式下，笔记依赖**从 PPTX 提取的文字 + 语音转写**；需要「看图」请用支持视觉的 API 模型。
2. **ICC 插件与版本绑定。** 插件按 ICC CE 1.7.18.8 的 `EnhancedPluginBaseV2` 接口、.NET Framework 4.7.2 编译，已在真机验证加载、启用、事件推送。ICC 升级到 1.8.x 后接口可能变化，需要按 `integrations/icc-bridge` 里的工程重新编译（`build.ps1` 一条命令）。即使插件不生效，ClassNote 自己的 COM / 窗口检测仍然工作，功能不受影响。
3. **WPS 的 COM 支持取决于配置。** WPS 需在「配置工具 → 兼容设置」里勾选「WPS Office 兼容第三方系统和软件」，否则拿不到 `KWPP.Application`，此时会退回窗口截图。
4. **PPT 文本提取只支持 `.pptx`。** 旧的 `.ppt` 二进制格式读不了，会用截图 + 模型看图代替。
5. **语音转写已随包自带。** whisper.cpp 与 base-q5_1 模型随程序分发，首次启动自动部署到 `C:\ProgramData\ClassNote\whisper`（whisper 是 C 程序、读不懂中文路径，所以要放在 ASCII 目录）并常驻运行，完全离线、无需安装。要更准的模型在设置里一键下载。
   - 包内带 `ggml-cpu-alderlake / haswell / skylakex …` 等全部 CPU 后端，whisper 会按 CPU 指令集自动挑最快的；只带通用 `x64` 后端会慢约 15 倍。
   - 实测 i5-1240P：4.6 秒中文语音约 3 秒转写完成；线程数按逻辑核心数自动设置（上限 16）。
   - 每批录音默认最长 10 秒。到达上限时不会硬切，而是回溯到最近一次句间停顿，把最后一句并入下一批，当前批次立即送转写；持续连读、完全没有停顿时才退回硬切。
   - 中文识别对同音字（如「荷塘月色」→「和堂乐色」）仍有误差，属本地小模型的正常水平；要更高精度可换 medium 模型或用云端接口。
6. **DeepSeek 网页版在某些网络下被限流。** 本机 curl 请求 chat.deepseek.com 会返回 429，但真实浏览器可以正常打开登录页；接入流程与千问一致（设置 → 模型 → 网页版 → 登录）。API 模式的 DeepSeek 预设不受影响。
7. **只在 Windows 上测试。** 程序本身使用了 Win32 API，macOS / Linux 无法编译运行（`internal/plugin`、`internal/agent`、`internal/llm`、`internal/update` 这些纯逻辑包可以在 Linux 上跑单测）。
8. **DSH 插件跑在 DSH 里，不跑在 ClassNote 里。** ClassNote 负责装运行时、装插件、起进程、把界面嵌进来；插件代码本身在 DSH 的进程与页面里执行，跨源隔离，碰不到 ClassNote 的数据，也碰不到你的课堂文件。这是刻意划的边界。
9. **DSH 索引是快照。** 内置的那份是打版时的状态，之后新上架的插件要等后台刷新（24 小时一次）才会出现；断网时一直用快照。

---

## 常见问题

**Q：双击没反应？**
看 `data/logs/` 下最新的日志。常见原因是端口被占用（换 `-port`）或程序目录没有写权限（不要放 `Program Files`）。

**Q：能识别 WPS 吗？**
能，但需要 WPS 打开 COM 兼容开关（见上）。检测逻辑同时匹配 `wpp.exe` / `wps.exe` 的放映窗口。

**Q：为什么笔记里写「本页缺少语音证据」？**
说明这一页没有可用的转写（麦克风没开、环境太吵、或 ASR 没配置）。程序不会在这种情况下编造老师说过的话，照搬分也会标为「未判定」。

**Q：报告里的照搬分是怎么算的？**
每页由模型给出 0–100 的分与证据，整堂课按转写长度加权平均。阈值：≥70 疑似照搬、40–69 部分照搬、<40 以讲解为主。没有语音的页不计入。

**Q：数据会上传吗？**
只有你配置的模型接口会收到「画面文字 + 转写文本」（勾选视觉时还有截图）。课堂记录、截图、报告都只在本机。

---

## 更新日志

### v1.8.1（当前）

**语音模型库**：模型不再写死在安装包里，改成可管理、可下载、可切换的一份清单。

| 能力 | 说明 |
| --- | --- |
| 五种模型 | Tiny 32 MB / Base 60 MB / Small 190 MB / Medium 539 MB / Large v3 Turbo 574 MB，都标了中文准确率与相对速度 |
| 一键下载 | 后台跑，界面显示百分比、速度、剩余时间；可取消，取消后保留已下部分 |
| 断点续传 | 下次点「继续下载」从 `.part` 的末尾接着来，不用重下几十上百 MB |
| 多源回落 | 默认 `hf-mirror.com`（国内）→ `huggingface.co`（官方），每源重试两次；也可在页面里手动指定 |
| SHA-256 校验 | 下完比对官方值，不一致自动删掉重下一次 |
| 无进展看门狗 | 45 秒收不到任何字节就判定卡死、换源重试——不会让界面永远停在 0% |
| 即时切换 | 点「使用」就重启 whisper 服务加载新模型，不用重启程序 |
| 删除 | 释放空间；正在用的那个拒绝删除 |

**发布包自带 `ggml-base-q5_1.bin`（60 MB）**，所以断网机器也能开箱转写。没有模型的裁剪版，首次启动会在后台自动下载 Base，不阻塞启动。

**同时修掉的**

| 问题 | 后果 |
| --- | --- |
| `-l zh` 写死 | 英语课、日语课被强行转成汉字。改成按配置的语言，设置里可选中文/英语/日语/自动 |
| 默认 `mode` 是 `api` 且预填了 BaseURL | 新装用户默认走云端，但没 Key 就用不了，语音整条链路默认是坏的。默认改为 `local` |
| `RestartLocal` 缺位 | 换模型时旧进程不被替换，`StartLocal` 会「复用」旧实例，表现为换完没变化。新增停-等-起三步 |
| `localSrv` 无锁读写 | 换模型与转写并发时是数据竞争 |
| 退出不停 whisper-server | 独立进程会活到下次启动，占着 18095 端口 |

**自检**：`internal/asrmodel` 12 个用例（续传、校验失败重下、取消保留 `.part`、多源回落、清单自洽、路径注入）；真实网络下完整下载 Tiny（32 MB / 13.6 s）与 Base（60 MB / 95 s），SHA-256 全部通过。

### v1.2.8

**新增：内置 DSH 运行时**

- 插件页顶部一键装 **Node + DeepSeek Harness**，装完即可在 ClassNote 里安装、启停、运行 DSH 的插件。
- DSH 渠道的按钮从「复制命令」变成 **安装到 DSH**：真的执行 `dsh plugin --profile web add`，装完同步重启运行时生效。
- DSH 的 Web UI 可以直接 **iframe 嵌在插件页里**（实测其响应没有 `X-Frame-Options`，可嵌），也能丢给系统浏览器。
- 没有 Node 时自动下载官方便携版（约 35 MB，解压到 `data/dsh/node`，不写系统 PATH）；装插件用的 pnpm 由 corepack 生成，同样不污染系统。
- 识别 DSH 的**版本兼容守卫**：不兼容的插件先说明「要求 X / 当前 Y」，用户确认后才用 `allow-version --accept-risk` 强行装。
- 装完在本地留一份 **MIT 许可原文**与来源说明，满足署名义务。

**修掉的坑**（都是这轮联调实测出来的，不是推演）

| 问题 | 后果 |
| --- | --- |
| Node 22.13 跑 DSH 会静默退出（`import.meta.main`） | 表面「装好了但没反应」，实际是版本太低。现在先探测版本并说人话 |
| 停止 DSH 会永久卡住 | 子进程没独立进程组，`kill(-pgid)` 打到自己；`Wait()` 没有超时。现在 `Setpgid` + 5 秒超时 + 强杀 |
| 装完插件用后台 goroutine 重启 | 和外部 `Stop()` 抢进程，留下占端口的孤儿。改成同步重启 |
| 日志缓冲无锁 | 多个协程同时写 `logs`，`Status()` 并发读是数据竞争 |
| `Shutdown()` 里调 `dshOnce.Do(空函数)` | once 被标记完成，之后再也创建不出运行时 |

**自检**：`internal/dsh` 9 个用例（版本比较、令牌解析、插件列表解析、zip slip、许可落盘、包名注入）；真机跑通「从零安装 1 分 53 秒 → 启动 1.4 秒 → 装 3 个插件 → 停止无残留」全链路。

### v1.2.7

**新增**

- **插件模型 v2**：插件从「一段提示词」升级为带身份、类型、能力面、最小权限、界面槽位、事件订阅的声明式清单。v1 文件自动迁移，不用手改。
- **插件市场双渠道**：「ClassNote」装本地插件即装即用；「DSH」可浏览 DeepSeek Harness 的 4104 个插件（内置快照，断网可用），复制安装命令或登记到本机。
- **课堂 Agent 重做**：真实的 turn/step 工具循环 + 8 个内置工具 + 权限闸门 + 一次性审批 + 重复调用检测 + 步数上限；模型列表自带网页版 Qwen / DeepSeek，免 API Key。
- **界面样式切换器**：经典 / DSH 两种布局一键切换，功能不分叉；DSH 样式的来源在界面与本文档均有标注。
- **插件界面槽位**：插件可以往状态条、仪表盘、设置页、Agent 页四个固定槽位投放卡片。

**修复**

- 市场每次搜索都同步请求 GitHub：断网时每敲一个字卡十几秒 → 改为缓存 + 后台刷新，接口永不等待网络。
- 装了插件不重启不生效 → 插件变更后立刻把提示词同步给分析引擎。
- Agent 审批凭证用时间戳拼接，本机任意网页都能猜出来并伪造批准 → 改用 `crypto/rand`。
- 模型可以自己编一个 `sessionId` 去读别的班的课堂记录 → 会话作用域改由宿主注入，并加测试锁死。
- 市场索引地址可通过 query 指定，等于让本机代打内网 → 只允许 https，且拒绝回环 / 私网 / 链路本地地址。
- 关掉悬浮球后被配置归一化强行打开；全屏放映时悬浮球压住课件；上课期间弹「有新版本」气泡 → 三处分别修掉。
- 插件列表里的启用开关用了 `<span>`，而样式只认 `<i>`，导致开关整块不可见 → 修正。
- 会话 ID 只挡了斜杠，`..` 与盘符冒号仍可拼进路径 → 收紧为白名单字符。
- `internal/app` 被服务端与更新检查引用，把整条依赖链锁死在 Windows 上 → 抽出 `internal/appinfo`，`internal/update` 现在能在 Linux/CI 上编译与测试。

**自检**

- 单测 20 个用例（插件 10 + Agent 10）在 Linux 上全绿；`GOOS=windows go build ./...` 与 `go vet ./...` 通过。
- 新增 `internal/server/smoke_test.go`：在临时目录起一套完整服务端，把首页、静态资源、bootstrap、status、会话、主题、插件 v2 清单、双渠道市场、槽位、Agent 模型目录与权限边界、样式切换全部真跑一遍。**在 Windows 上执行 `go test ./internal/server/` 即可复跑。**
- 新增 `cmd/verify` 的四项运行时检查：插件清单版本、Agent 工具与模型、市场双渠道、样式切换写读回。

### v1.2.6.1 及以前

见 [Releases](https://github.com/tangyuan9325/ClassNote/releases)。

---

## 开源许可

本项目以 **MIT License** 发布，详见 [LICENSE](LICENSE)。

第三方组件：

| 组件 | 用途 | 许可 |
| --- | --- | --- |
| [go-rod/rod](https://github.com/go-rod/rod) | 浏览器自动化（网页版 AI、PDF 打印） | MIT |
| [go-ole/go-ole](https://github.com/go-ole/go-ole) | PowerPoint / WPS 的 COM 调用 | MIT |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Win32 API 绑定 | BSD-3-Clause |
| [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) | **仅借鉴设计**：界面布局与视觉规范、插件模型与市场索引格式、Agent 的 turn/step 与工具审批流程。未包含其源代码 | MIT |
| [dsh-plugin-catalog](https://www.npmjs.com/package/dsh-plugin-catalog) | DSH 插件索引快照（`internal/plugin/dsh-plugins.json.gz`，4104 条） | MIT |
| [DeepSeek Harness 运行时](https://www.npmjs.com/package/@deepseek-ai/dsh) | 按需安装到用户本机（`data/dsh/runtime`），**不随本项目分发**；装完在本地保留 MIT 许可原文 | MIT |
| [Node.js](https://nodejs.org) | 按需下载的便携运行时（`data/dsh/node`），仅在启用 DSH 时使用 | MIT |

关于 DSH 的借鉴范围，说明白一点，免得误会：

- **参考了设计，没有搬代码。** 界面 CSS 是按 DSH 的度量与层级规范重写的；插件清单结构、市场索引格式是按它的公开契约对齐的，方便以后两边互通。
- **没有引入 DSH 运行时。** ClassNote 不是 DSH 的分支，也不加载 npm 包；DSH 渠道的插件在 ClassNote 里只是「引用 + 安装命令」。
- 如果 DSH 的维护者认为某处标注不够清楚，开个 issue 即可，会立刻调整。

ClassNote 与 ClassIsland、InkCanvasForClass、WPS、Microsoft Office 均无隶属关系，仅在本地通过公开接口与之协作。

---

<p align="center"><sub>ClassNote · 让每一页板书都留下痕迹</sub></p>
