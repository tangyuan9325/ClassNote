# ClassNote 集成接口

所有接口都在 `http://127.0.0.1:<port>`（默认 41735）上，只监听回环地址。
端口与 pid 记录在程序目录的 `data/runtime.json`。

## 1. InkCanvasForClass 事件桥

ICC 的放映/翻页事件可以由插件、脚本或任何能发 HTTP 的程序推送过来。
ClassNote 会把它合并进当前课堂状态；即使没有事件，放映检测也照常工作。

```http
POST /api/integrations/icc/event
Content-Type: application/json

{
  "event": "slide_changed",
  "presenting": true,
  "slide": 7,
  "source": "icc-plugin"
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `event` | string | 事件名，建议使用 `slideshow_start` / `slide_changed` / `slideshow_end` / `annotating` |
| `presenting` | bool | 是否正在放映，缺省则沿用上一次状态 |
| `slide` | number | 当前页码，可选 |
| `source` | string | 推送方标识，便于排查 |

返回：`{"ok": true}`

### 可用的推送方式

1. **ICC 插件**：`plugins/ClassNote.Bridge.dll`（源码在 `integrations/icc-bridge`）。
   它继承 `Ink_Canvas.Helpers.Plugins.EnhancedPluginBaseV2`，在 `Initialize/Enable/OnStartup`
   里起一个 1 秒定时器，检测到放映状态或页码变化时推送，另外每 20 秒发一次心跳。
   事件名：`icc_ppt_state`（放映中）、`icc_idle`（未放映）、`icc_shutdown`（ICC 退出）。
2. **外部脚本**：任何能发 HTTP 的自动化工具（AutoHotkey、Python、PowerShell）都可以。

```powershell
# PowerShell 示例
$body = @{ event = "slide_changed"; presenting = $true; slide = 3 } | ConvertTo-Json
Invoke-RestMethod -Uri "http://127.0.0.1:41735/api/integrations/icc/event" `
  -Method Post -ContentType "application/json" -Body $body
```

## 2. ClassIsland 状态推送

如果你的课表不在 ClassIsland 里，也可以直接把「现在上什么课」推给 ClassNote：

```http
POST /api/integrations/classisland/state
Content-Type: application/json

{
  "subject": "语文",
  "inClass": true,
  "start": "09:10",
  "end": "09:50",
  "room": "高二(3)班"
}
```

推送的状态在 90 秒内有效，之后回落到本地课表解析。
配合设置里的「上课自动开始」，就能实现「任何课表系统 → 自动记录」。

## 3. 课堂控制

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/lesson/start` | `{"subject":"语文","title":"...","source":"manual"}` |
| POST | `/api/lesson/stop` | `{"report":true}` 结束时顺便生成报告 |
| POST | `/api/lesson/capture` | 立即截取当前画面并新开一个片段 |
| POST | `/api/lesson/utterance` | `{"text":"..."}` 手动补录一段转写 |
| POST | `/api/lesson/ask` | `{"question":"..."}` 就本堂课提问 |
| POST | `/api/lesson/report` | 生成报告（`sessionId` 可选，缺省用当前课堂） |

## 4. 事件流

`GET /api/events` 是 SSE，事件类型：

`session`、`session_ended`、`slide`、`transcript`、`analysis`、`segment_status`、
`report`、`error`、`class_state`、`auto_start`、`auto_stop`、`icc`、`classisland`

```js
const es = new EventSource('http://127.0.0.1:41735/api/events');
es.addEventListener('analysis', (e) => {
  const { segment } = JSON.parse(e.data).data;
  console.log(segment.index, segment.summary, segment.copyScore);
});
```

## 5. 查询

| 路径 | 说明 |
| --- | --- |
| `GET /api/status` | 引擎状态 + 课表状态 + ICC 状态 |
| `GET /api/session` | 当前课堂（含片段、笔记、对话） |
| `GET /api/session?id=<id>` | 指定课堂 |
| `GET /api/sessions` | 历史课堂列表 |
| `GET /api/timetable` | 今日课表 |
| `GET /api/env` | 环境自检结果 |
| `GET /api/logs?n=200` | 最近日志 |

## 6. 鉴权

接口只绑定 `127.0.0.1`，默认不做鉴权——这与本机的其它本地服务一致。
如果机器上有不受信任的本地用户，请自行在防火墙或 hosts 层限制访问。
