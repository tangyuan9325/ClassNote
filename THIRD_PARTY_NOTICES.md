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

## 运行环境依赖

- **Microsoft Edge / Google Chrome**：用于渲染界面窗口与把报告 HTML 打印为 PDF。
  程序只调用其公开的 `--headless` 与 DevTools 协议，不修改浏览器配置。
- **Microsoft Office PowerPoint / WPS Office**（可选）：通过公开的 COM 自动化接口
  读取放映页码并导出当前幻灯片。未安装时自动退回屏幕截图。
- **ClassIsland**（可选）：只读取其用户目录下的课表 JSON，不修改任何文件。
- **InkCanvasForClass CE**（可选）：只读取进程信息与安装目录，事件桥通过本地 HTTP 推送。

## 商标声明

ClassIsland、InkCanvasForClass、WPS Office、Microsoft Office、Qwen、DeepSeek
均为其各自所有者的商标。本项目与上述项目/公司无隶属关系，也未获得其背书。
