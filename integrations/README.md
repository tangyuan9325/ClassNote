# 集成扩展

这个目录用于放置与外部软件的桥接组件。

## ClassIsland 注入插件

源码在 `classisland-bridge/`，编译产物是 `ClassNote.IslandBridge.cnx`（zip 包，内含 DLL + manifest + 图标），
由 ClassNote 内嵌，设置 → 集成 → 「安装 / 更新插件」会解压到 `<ClassIsland 数据目录>/Plugins/classnote.islandbridge/`。

- 目标框架：net8.0，ClassIsland API 2.0（已在 2.1.0.1 上验证加载）
- 状态接口：`http://127.0.0.1:51888/state`（端口被占用时自动顺延）
- 通知接口：`POST /notify`，由插件转交 ClassIsland 的通知宿主
- 重新编译：`dotnet build -c Release`

## InkCanvasForClass CE 事件桥

插件源码在 `icc-bridge/`，编译产物随程序发布在 `plugins/ClassNote.Bridge.dll`。
它按 InkCanvasForClass CE 1.7.18.8 的 `EnhancedPluginBaseV2` 接口、.NET Framework 4.7.2 编译，
已在本机验证：ICC 启动时加载、自动启用、放映开始/结束时各推送一次事件。

重新编译（ICC 升级后可能需要）：`build.ps1 -IccDir "<ICC 目录>"`。

插件做的事情很简单：把状态 POST 到 ClassNote：

```http
POST http://127.0.0.1:41735/api/integrations/icc/event
Content-Type: application/json

{ "event": "slide_changed", "presenting": true, "slide": 7 }
```

完整协议见 [../docs/INTEGRATIONS.md](../docs/INTEGRATIONS.md)。

## 编译好的 DLL 放哪里

- 随程序分发：放到程序目录的 `plugins/ClassNote.Bridge.dll`，
  界面上的「注入插件」会自动找到它并复制到 ICC 的 `Plugins` 目录。
- 手动安装：直接复制到 `<ICC 安装目录>/Plugins/`，重启 ICC 生效。

## 不做插件也能用

放映检测不依赖 ICC：ClassNote 直接调用 PowerPoint / WPS 的 COM 接口读取页码并导出画面，
或用 `PrintWindow` 抓取放映窗口。ICC 开着只会让屏幕截图带上批注，而 COM 导出不受影响。
