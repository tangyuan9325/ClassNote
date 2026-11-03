package asr

import "classnote/internal/asrmodel"

// 模型清单、下载与校验都在 internal/asrmodel 里。
//
// 拆出去的原因：本包依赖 internal/audio（Win32 录音），只能在 Windows 编译；
// 而下载器是纯逻辑，必须能在任何平台上跑测试。这里只做转发，
// 调用方仍然从 asr 这里取，不用关心底下分了几个包。

// Model 是一个可下载的 whisper.cpp 模型。
type Model = asrmodel.Model

// ModelStatus 是清单某一项的本地状态。
type ModelStatus = asrmodel.ModelStatus

// Progress 是一次下载的进度。
type Progress = asrmodel.Progress

// ModelManager 管理模型下载。
type ModelManager = asrmodel.Manager

// ASRStatus 是语音模块的整体状态。
type ASRStatus = asrmodel.Status

// Models 是内置模型清单。
var Models = asrmodel.Models

// DefaultModelID 是首次运行自动部署的模型。
const DefaultModelID = asrmodel.DefaultModelID

// FindModelByFile 按文件名反查清单项。
func FindModelByFile(base string) (Model, bool) { return asrmodel.FindModelByFile(base) }

// FindModel 按 ID 查清单。
func FindModel(id string) (Model, bool) { return asrmodel.FindModel(id) }

// SetRuntimeDir 注入 whisper 运行时的实际目录。
func SetRuntimeDir(dir string) { asrmodel.SetRuntimeDir(dir) }

// RuntimeDir 返回运行时目录。
func RuntimeDir() string { return asrmodel.RuntimeDir() }

// LocalModelPath 返回模型在本机的落地路径。
func LocalModelPath(m Model) string { return asrmodel.LocalModelPath(m) }

// HasModel 判断模型是否已下载完整。
func HasModel(m Model) bool { return asrmodel.HasModel(m) }

// NewManager 创建下载管理器。
func NewManager(notify func(Progress)) *ModelManager { return asrmodel.NewManager(notify) }

// OnModelReady 注册模型下载完成后的回调。
func OnModelReady(fn func(Model)) { asrmodel.OnModelReady(fn) }
