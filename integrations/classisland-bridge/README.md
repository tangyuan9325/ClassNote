# ClassNote 课记联动

把 ClassIsland 的课堂状态注入 [ClassNote（课记）](https://github.com/tangyuan9325/ClassNote)。

## 功能

- **精确的上课状态**：直接读取 ClassIsland 当前课表，推送科目、起止时间、下一节课。
- **事件驱动**：上课 / 下课 / 放学时立即刷新，不再依赖轮询课表文件。
- **课堂提醒**：ClassNote 生成报告或需要提醒时，可以让 ClassIsland 弹出通知。
- **本地回环**：状态接口只监听 `127.0.0.1:51888`，不对外网开放。

## 安装

在 ClassNote 的「设置 → 集成 → ClassIsland 注入联动」里点「安装插件」，
或把整个目录复制到 ClassIsland 的 `data/Plugins/classnote.islandbridge/` 后重启 ClassIsland。

## 接口

- `GET http://127.0.0.1:51888/ping`：插件存活检测
- `GET http://127.0.0.1:51888/state`：当前课程状态
- `POST http://127.0.0.1:51888/notify`：`{"title":"…","body":"…"}` 弹出通知

## 日志

`data/Plugins/classnote.islandbridge/bridge.log`（ClassIsland 配置目录下）。
