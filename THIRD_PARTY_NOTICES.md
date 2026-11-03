# 第三方组件与许可

本项目自身以 MIT 许可发布。以下依赖通过 Go modules 引入，均为宽松许可，
分发二进制时请保留各自的版权声明。

| 模块 | 版本 | 用途 | 许可 |
| --- | --- | --- | --- |
| github.com/go-rod/rod | v0.116.2 | 浏览器自动化：网页版 AI 对话、PDF 打印 | MIT |
| github.com/go-ole/go-ole | v1.3.0 | PowerPoint / WPS 的 COM 接口绑定 | MIT |
| golang.org/x/sys | v0.31.0 | Win32 API 绑定 | BSD-3-Clause |
| github.com/ysmood/gson | v0.7.3 | rod 的 JSON 处理依赖 | MIT |
| github.com/ysmood/goob | v0.4.0 | rod 的观察者依赖 | MIT |
| github.com/ysmood/leakless | v0.9.0 | rod 的子进程管理依赖 | MIT |
| github.com/ysmood/fetchup | v0.2.3 | rod 的下载依赖 | MIT |
| github.com/ysmood/got | v0.40.0 | rod 的测试依赖 | MIT |

## 设计借鉴（未包含源代码）

| 项目 | 借鉴内容 | 许可 |
| --- | --- | --- |
| [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) | 界面布局与视觉规范（栅格比例、圆角阶梯、发丝线、浮起面板层级）、插件清单与市场索引契约、Agent 的 turn/step 与工具审批流程 | MIT |
| [dsh-plugin-catalog](https://www.npmjs.com/package/dsh-plugin-catalog)（npm） | 插件索引快照，随包分发的 `internal/plugin/dsh-plugins.json.gz` 由此生成 | MIT |

这两个项目只被**参考设计与公开契约**，没有代码被复制进本仓库。

## 随包分发的第三方二进制

| 组件 | 说明 | 许可 |
| --- | --- | --- |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | `integrations/whisper/` 下的 `whisper-server.exe`、`whisper-cli.exe`、`whisper-stream.exe`、`whisper.dll`、`ggml*.dll`、`SDL2.dll`（其中 SDL2 为 zlib 许可） | MIT |
| [whisper 模型](https://huggingface.co/ggerganov/whisper.cpp) | `integrations/whisper/models/ggml-base-q5_1.bin`，由 OpenAI Whisper 权重转换并量化 | MIT（模型权重 MIT，whisper.cpp 仓库同许可） |

whisper.cpp 的版权声明为 Copyright (c) 2023-2024 The ggml authors。
本项目只调用其可执行文件的标准命令行与 HTTP 接口，未修改其源代码；
完整许可文本见 <https://github.com/ggml-org/whisper.cpp/blob/master/LICENSE>。

设置页里额外下载的模型（Small / Medium / Large v3 Turbo 等）来自同一仓库，
许可相同，下载后存放在 `integrations/whisper/models/`。

## 按需安装的运行时（不分发）

| 组件 | 说明 | 许可 |
| --- | --- | --- |
| [DeepSeek Harness](https://www.npmjs.com/package/@deepseek-ai/dsh) | 用户在设置里点「安装 DSH 运行时」时，由本机 npm 从公共 registry 安装到 `data/dsh/runtime`。本项目不分发其代码 | MIT（许可原文随安装落到该目录） |
| [Node.js](https://nodejs.org) | 本机没有满足版本要求的 Node 时，下载官方便携 zip 解压到 `data/dsh/node` | MIT |

MIT 允许再分发，但要求在副本中保留版权声明与许可声明。因此运行时安装完成后，
ClassNote 会在 `data/dsh/runtime/` 写下 `LICENSE-DeepSeek-Harness.txt`（原文）与
`README-ClassNote.txt`（来源与边界说明）；Node 便携版自带 LICENSE 文件，一并保留。

插件是各自作者的独立作品，以各自许可发布。ClassNote 只负责执行
`dsh plugin --profile web add <包名>`，不对插件内容作任何担保。

## 运行环境依赖

- **Microsoft Edge / Google Chrome**：用于渲染界面窗口与把报告 HTML 打印为 PDF。
  程序只调用其公开的 `--headless` 与 DevTools 协议，不修改浏览器配置。
- **Microsoft Office PowerPoint / WPS Office**（可选）：通过公开的 COM 自动化接口
  读取放映页码并导出当前幻灯片。未安装时自动退回屏幕截图。
- **ClassIsland**（可选）：只读取其用户目录下的课表 JSON，不修改任何文件。
- **InkCanvasForClass CE**（可选）：只读取进程信息与安装目录，事件桥通过本地 HTTP 推送。

## 商标声明

ClassIsland、InkCanvasForClass、WPS Office、Microsoft Office、Qwen、DeepSeek、
DeepSeek Harness 均为其各自所有者的商标。本项目与上述项目/公司无隶属关系，也未获得其背书。
