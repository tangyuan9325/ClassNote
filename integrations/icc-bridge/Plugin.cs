using System;
using System.Diagnostics;
using System.Globalization;
using System.Net;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Windows.Controls;
using Ink_Canvas.Helpers.Plugins;

namespace ClassNote.Bridge
{
    /// <summary>
    /// 把 InkCanvasForClass 的运行状态推送给本机的 ClassNote。
    /// 只做一件事：定时读取 ICC 的放映状态，变化时发一个 HTTP 请求。
    /// 不修改 ICC 的任何状态，也不依赖 ICC 的内部实现细节。
    /// </summary>
    public class ClassNoteBridgePlugin : EnhancedPluginBaseV2
    {
        private const string PluginVersion = "1.0.0";

        private readonly object _lock = new object();
        private Timer _timer;
        private int _lastPresenting = -1;
        private int _lastPage = -1;
        private int _port;
        private int _failCount;
        private DateTime _lastProbeLog = DateTime.MinValue;
        private DateTime _lastSend = DateTime.MinValue;

        public override string Name => "ClassNote 联动";
        public override string Description => "把放映状态与页码实时推送给 ClassNote，用于课堂自动记录与逐页笔记。";
        public override Version Version => new Version(1, 0, 0);
        public override string Author => "ClassNote";
        public override bool IsBuiltIn => false;

        // ICC 的不同版本可能调用 Initialize / Enable / OnStartup 中的一个或多个，
        // 这里统一走幂等的 StartBridge，避免漏启动。
        public override void Initialize()
        {
            base.Initialize();
            StartBridge("Initialize");
        }

        public override void Enable()
        {
            base.Enable();
            StartBridge("Enable");
        }

        public override void Disable()
        {
            StopBridge();
            base.Disable();
        }

        public override void Cleanup()
        {
            StopBridge();
            base.Cleanup();
        }

        public override void OnStartup() => StartBridge("OnStartup");

        public override void OnShutdown() => StopBridge();

        public override void OnConfigurationChanged() { }

        public override string GetStatusBarInfo()
        {
            if (_port <= 0) return "ClassNote: 未连接";
            return "ClassNote: 已连接";
        }

        public override MenuItem[] GetMenuItems() => new MenuItem[0];

        public override Button[] GetToolbarButtons() => new Button[0];

        private void StartBridge(string from)
        {
            try
            {
                lock (_lock)
                {
                    if (_timer != null) return;
                    _port = FindPort();
                    _timer = new Timer(Tick, null, 800, 1000);
                }
                BridgeLog.Write("已启动(" + from + ")，ClassNote 端口 " +
                    (_port > 0 ? _port.ToString(CultureInfo.InvariantCulture) : "未找到"));
            }
            catch (Exception ex)
            {
                BridgeLog.Write("启动失败(" + from + "): " + ex.Message);
            }
        }

        private void StopBridge()
        {
            try
            {
                Timer t;
                lock (_lock)
                {
                    t = _timer;
                    _timer = null;
                }
                if (t == null) return;
                t.Dispose();
                Send("icc_shutdown", false, 0);
                BridgeLog.Write("已停止");
            }
            catch { /* 退出阶段忽略异常 */ }
        }

        private void Tick(object state)
        {
            try
            {
                if (_timer == null) return;
                var svc = GetService;
                if (svc == null) return;

                // ICC 1.7.x 的 IsPPTMode 在部分机器上即使已连接放映仍为 false，
                // 因此再叠加一层自己的窗口探测，保证推送出去的状态是真实的。
                bool detected = SlideShowDetector.IsRunning();
                bool presenting = detected;
                int page = 0;
                try
                {
                    presenting = presenting || svc.IsPPTMode;
                    page = svc.CurrentPageIndex + 1;
                }
                catch { /* 服务未就绪时忽略 */ }

                if ((DateTime.Now - _lastProbeLog).TotalSeconds >= 30)
                {
                    _lastProbeLog = DateTime.Now;
                    string detail;
                    try
                    {
                        detail = string.Format(CultureInfo.InvariantCulture,
                            "probe detected={0} ppt={1} wb={2} fs={3} page={4}/{5} canvas={6}",
                            detected, svc.IsPPTMode, svc.IsWhiteboardMode, svc.IsFullScreenMode,
                            svc.CurrentPageIndex, svc.TotalPageCount, svc.IsCanvasMode);
                    }
                    catch (Exception ex) { detail = "probe-error " + ex.Message; }
                    BridgeLog.Write(detail);
                }

                int flag = presenting ? 1 : 0;
                bool changed = flag != _lastPresenting || page != _lastPage;
                // 即使状态没变也每 20 秒补一次心跳，ClassNote 重启后能立即恢复状态。
                bool heartbeat = (DateTime.Now - _lastSend).TotalSeconds >= 20;
                if (!changed && !heartbeat) return;
                _lastPresenting = flag;
                _lastPage = page;

                if (_port <= 0 || _failCount > 20) _port = FindPort();
                if (_port <= 0) return;

                if (Send(presenting ? "icc_ppt_state" : "icc_idle", presenting, page))
                {
                    _lastSend = DateTime.Now;
                }
            }
            catch (Exception ex)
            {
                BridgeLog.Write("轮询异常: " + ex.Message);
            }
        }

        private bool Send(string name, bool presenting, int page)
        {
            if (_port <= 0) return false;
            string body = string.Format(CultureInfo.InvariantCulture,
                "{{\"event\":\"{0}\",\"presenting\":{1},\"slide\":{2},\"source\":\"icc-plugin\",\"pluginVersion\":\"{3}\"}}",
                name, presenting ? "true" : "false", page, PluginVersion);
            try
            {
                using (var wc = new WebClient())
                {
                    wc.Headers[HttpRequestHeader.ContentType] = "application/json; charset=utf-8";
                    wc.Encoding = Encoding.UTF8;
                    wc.UploadString("http://127.0.0.1:" + _port + "/api/integrations/icc/event", "POST", body);
                }
                _failCount = 0;
                return true;
            }
            catch
            {
                _failCount++;
                if (_failCount == 5) _port = FindPort();
                return false;
            }
        }

        /// <summary>ClassNote 可能因端口占用而换端口，这里在固定范围内探测一次。</summary>
        private static int FindPort()
        {
            for (int p = 41735; p < 41745; p++)
            {
                try
                {
                    var req = (HttpWebRequest)WebRequest.Create("http://127.0.0.1:" + p + "/api/status");
                    req.Timeout = 300;
                    req.Method = "GET";
                    using (var resp = (HttpWebResponse)req.GetResponse())
                    {
                        if ((int)resp.StatusCode == 200) return p;
                    }
                }
                catch { /* 该端口没有服务 */ }
            }
            return 0;
        }
    }

    /// <summary>通过窗口特征判断 PowerPoint / WPS 是否正在放映。</summary>
    internal static class SlideShowDetector
    {
        private delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

        [DllImport("user32.dll")]
        private static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
        [DllImport("user32.dll")]
        private static extern bool IsWindowVisible(IntPtr hWnd);
        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        private static extern int GetClassName(IntPtr hWnd, StringBuilder lpClassName, int nMaxCount);
        [DllImport("user32.dll")]
        private static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint lpdwProcessId);
        [DllImport("user32.dll")]
        private static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);

        [StructLayout(LayoutKind.Sequential)]
        private struct RECT { public int Left, Top, Right, Bottom; }

        public static bool IsRunning()
        {
            bool found = false;
            try
            {
                EnumWindows((hWnd, lParam) =>
                {
                    if (found || !IsWindowVisible(hWnd)) return true;
                    uint pid;
                    GetWindowThreadProcessId(hWnd, out pid);
                    if (pid == 0) return true;

                    string proc;
                    try { proc = Process.GetProcessById((int)pid).ProcessName.ToLowerInvariant(); }
                    catch { return true; }
                    if (proc != "powerpnt" && proc != "wpp" && proc != "wps" && proc != "pptview") return true;

                    var sb = new StringBuilder(256);
                    GetClassName(hWnd, sb, sb.Capacity);
                    string cls = sb.ToString();
                    RECT r;
                    GetWindowRect(hWnd, out r);
                    int w = r.Right - r.Left, h = r.Bottom - r.Top;
                    bool fullscreen = w >= 1024 && h >= 700 && r.Left <= 2 && r.Top <= 2;

                    if (cls.Equals("screenClass", StringComparison.OrdinalIgnoreCase) ||
                        cls.IndexOf("slide", StringComparison.OrdinalIgnoreCase) >= 0 ||
                        (fullscreen && (cls.Equals("wpp", StringComparison.OrdinalIgnoreCase) ||
                                        cls.Equals("wps", StringComparison.OrdinalIgnoreCase))))
                    {
                        found = true;
                    }
                    return true;
                }, IntPtr.Zero);
            }
            catch { /* 探测失败按未放映处理 */ }
            return found;
        }
    }

    internal static class BridgeLog
    {
        public static void Write(string message)
        {
            try
            {
                string dir = System.IO.Path.Combine(
                    Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                    "ClassNoteBridge");
                System.IO.Directory.CreateDirectory(dir);
                System.IO.File.AppendAllText(
                    System.IO.Path.Combine(dir, "bridge.log"),
                    DateTime.Now.ToString("yyyy-MM-dd HH:mm:ss", CultureInfo.InvariantCulture) + " " + message + Environment.NewLine,
                    Encoding.UTF8);
            }
            catch { /* 日志失败不影响主流程 */ }
        }
    }
}
