using System.Net;
using System.Text;
using System.Text.Json;
using ClassIsland.Core.Abstractions.Services;
using ClassIsland.Core.Abstractions.Services.NotificationProviders;
using ClassIsland.Core.Models.Notification;
using ClassIsland.Shared;
using ClassIsland.Shared.Models.Profile;
using Microsoft.Extensions.Hosting;

namespace ClassNote.IslandBridge;

/// <summary>
/// 常驻服务：维护当前课程状态，并在本机回环地址上暴露一个只读接口，
/// 供 ClassNote 拉取状态、推送课堂提醒。
/// </summary>
public sealed class BridgeService : IHostedService
{
    private const int BasePort = 51888;
    private readonly ILessonsService _lessons;
    private readonly IProfileService _profile;
    private readonly HttpListener _listener = new();
    private readonly CancellationTokenSource _cts = new();
    private int _port;

    public BridgeService(ILessonsService lessons, IProfileService profile)
    {
        _lessons = lessons;
        _profile = profile;
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _lessons.OnClass += OnClass;
        _lessons.OnBreakingTime += OnBreaking;
        _lessons.OnAfterSchool += OnAfterSchool;

        for (var p = BasePort; p < BasePort + 12; p++)
        {
            try
            {
                _listener.Prefixes.Clear();
                _listener.Prefixes.Add($"http://127.0.0.1:{p}/");
                _listener.Start();
                _port = p;
                break;
            }
            catch (Exception ex)
            {
                BridgeLog.Write($"端口 {p} 启动失败：{ex.Message}");
            }
        }
        if (_port == 0)
        {
            BridgeLog.Write("没有可用端口，状态接口未启动（不影响 ClassIsland 本身）");
            return Task.CompletedTask;
        }
        BridgeLog.Write($"状态接口已启动 http://127.0.0.1:{_port}/");
        try
        {
            File.WriteAllText(Path.Combine(Plugin.ConfigFolder, "port.txt"), _port.ToString());
        }
        catch (Exception ex)
        {
            BridgeLog.Write("写端口文件失败：" + ex.Message);
        }
        _ = Task.Run(() => LoopAsync(_cts.Token), CancellationToken.None);
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken)
    {
        _lessons.OnClass -= OnClass;
        _lessons.OnBreakingTime -= OnBreaking;
        _lessons.OnAfterSchool -= OnAfterSchool;
        try { _cts.Cancel(); } catch { }
        try { _listener.Stop(); } catch { }
        return Task.CompletedTask;
    }

    private void OnClass(object? sender, EventArgs e)
    {
        var st = BuildState();
        BridgeLog.Write($"上课：{st.Subject} {st.Start}-{st.End}");
    }

    private void OnBreaking(object? sender, EventArgs e)
    {
        var st = BuildState();
        BridgeLog.Write($"下课：{st.Subject}，下一节 {st.Next} {st.NextStart}");
    }

    private void OnAfterSchool(object? sender, EventArgs e) => BridgeLog.Write("放学");

    private async Task LoopAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            HttpListenerContext ctx;
            try { ctx = await _listener.GetContextAsync().ConfigureAwait(false); }
            catch { break; }
            _ = Task.Run(() => HandleAsync(ctx), CancellationToken.None);
        }
    }

    private async Task HandleAsync(HttpListenerContext ctx)
    {
        var req = ctx.Request;
        var res = ctx.Response;
        try
        {
            // 只对回环页面放行 CORS，且不再用 "*"。
            // 1.2.5 里这里是 Access-Control-Allow-Origin: *，而 /state 不需要任何
            // 凭据，等于学生电脑上任意一个网页都能读出完整课表（班级、科目、上课
            // 时间），也能往 ClassIsland 里灌任意通知。跨站请求一定带 Origin，
            // 所以按 Origin 判断来源就够了，ClassNote 自己是原生的 HttpClient，
            // 不带 Origin，不受影响。
            var origin = req.Headers["Origin"];
            if (!string.IsNullOrEmpty(origin))
            {
                if (!IsLoopbackOrigin(origin))
                {
                    res.StatusCode = 403;
                    res.Close();
                    return;
                }
                res.Headers["Access-Control-Allow-Origin"] = origin;
                res.Headers["Vary"] = "Origin";
            }
            res.Headers["Access-Control-Allow-Headers"] = "Content-Type";
            res.Headers["Access-Control-Allow-Methods"] = "GET,POST,OPTIONS";
            if (req.HttpMethod == "OPTIONS")
            {
                res.StatusCode = 204;
                res.Close();
                return;
            }

            var path = req.Url?.AbsolutePath?.TrimEnd('/').ToLowerInvariant() ?? "";
            if (path == "/ping" || path == "")
            {
                await WriteJson(res, new { ok = true, host = "ClassIsland", plugin = "classnote.islandbridge", version = "1.0.0", port = _port });
                return;
            }
            if (path == "/state")
            {
                await WriteJson(res, BuildState());
                return;
            }
            if (path == "/notify" && req.HttpMethod == "POST")
            {
                // 表单提交属于"无需预检的简单请求"，只靠 CORS 挡不住，
                // 这里再要求 JSON：跨站页面发 application/json 必须先预检，
                // 而预检已经在上面的 Origin 校验里被拒掉了。
                var ctype = req.ContentType ?? "";
                if (!ctype.StartsWith("application/json", StringComparison.OrdinalIgnoreCase))
                {
                    res.StatusCode = 415;
                    await WriteJson(res, new { ok = false, error = "请使用 application/json 提交" });
                    return;
                }
                string body;
                using (var reader = new StreamReader(req.InputStream, Encoding.UTF8))
                    body = await reader.ReadToEndAsync();
                var title = "ClassNote";
                var text = body;
                try
                {
                    using var doc = JsonDocument.Parse(body);
                    if (doc.RootElement.TryGetProperty("title", out var t)) title = t.GetString() ?? title;
                    if (doc.RootElement.TryGetProperty("body", out var b)) text = b.GetString() ?? "";
                }
                catch { /* 纯文本也接受 */ }
                var result = await NotifyAsync(title, text);
                await WriteJson(res, new { ok = result == "ok", result });
                return;
            }

            res.StatusCode = 404;
            await WriteJson(res, new { ok = false, error = "not found" });
        }
        catch (Exception ex)
        {
            BridgeLog.Write("请求处理失败：" + ex.Message);
            try { res.StatusCode = 500; res.Close(); } catch { }
        }
    }

    /// <summary>判断 Origin 是否来自本机回环地址。</summary>
    private static bool IsLoopbackOrigin(string origin)
    {
        if (!Uri.TryCreate(origin, UriKind.Absolute, out var u)) return false;
        var h = u.Host;
        return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == "[::1]"
            || h.StartsWith("127.", StringComparison.Ordinal);
    }

    private static async Task WriteJson(HttpListenerResponse res, object data)
    {
        var raw = JsonSerializer.Serialize(data, new JsonSerializerOptions
        {
            Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
        });
        var bytes = Encoding.UTF8.GetBytes(raw);
        res.ContentType = "application/json; charset=utf-8";
        res.ContentLength64 = bytes.Length;
        await res.OutputStream.WriteAsync(bytes);
        res.Close();
    }

    private Task<string> NotifyAsync(string title, string body)
    {
        try
        {
            // ClassIsland 的 NotificationHostService 只注册为 INotificationHostService，
            // 真正的 ShowNotificationAsync 在实现类上，用反射调用以兼容 2.x。
            var host = IAppHost.TryGetService<INotificationHostService>();
            if (host == null) return Task.FromResult("notification host unavailable");
            var text = string.IsNullOrWhiteSpace(body) ? title : $"{title}\n{body}";
            var request = new NotificationRequest
            {
                MaskContent = new NotificationContent(text) { Duration = TimeSpan.FromSeconds(8) },
                OverlayContent = new NotificationContent(text) { Duration = TimeSpan.FromSeconds(8) },
            };
            var method = host.GetType().GetMethod(
                "ShowNotificationAsync",
                new[] { typeof(NotificationRequest), typeof(Guid), typeof(Guid) });
            if (method == null) return Task.FromResult("notification method unavailable");
            // 通知会一直等待展示结束（约十秒），这里立刻返回，避免 ClassNote 的请求被拖住。
            _ = Task.Run(async () =>
            {
                try
                {
                    if (method.Invoke(host, new object[] { request, Guid.Empty, Guid.Empty }) is Task task)
                    {
                        await task;
                    }
                    BridgeLog.Write("通知已展示：" + title);
                }
                catch (Exception ex)
                {
                    BridgeLog.Write("发送通知失败：" + ex.Message);
                }
            });
            return Task.FromResult("queued");
        }
        catch (Exception ex)
        {
            BridgeLog.Write("发送通知失败：" + ex.Message);
            return Task.FromResult(ex.Message);
        }
    }

    private StateDto BuildState()
    {
        var now = DateTime.Now;
        var state = new StateDto
        {
            Ok = true,
            Host = "ClassIsland",
            Version = "1.0.0",
            UpdatedAt = now.ToString("O"),
            TimeState = "none",
        };
        try
        {
            var profile = _profile.Profile;
            var plan = _lessons.GetClassPlanByDate(now, out _);
            if (plan == null) return state;
            state.ClassName = plan.Name;
            var lessons = new List<LessonDto>();
            foreach (var info in plan.Classes)
            {
                if (!info.IsEnabled) continue;
                var item = info.CurrentTimeLayoutItem;
                if (item == null) continue;
                var start = now.Date + item.StartTime;
                var end = now.Date + item.EndTime;
                if (end <= start) continue;
                var subject = "";
                try
                {
                    if (profile != null && profile.Subjects.TryGetValue(info.SubjectId, out var subj))
                        subject = subj.Name ?? "";
                }
                catch { /* ignore */ }
                if (string.IsNullOrWhiteSpace(subject)) continue;
                lessons.Add(new LessonDto
                {
                    Subject = subject,
                    Start = start.ToString("HH:mm"),
                    End = end.ToString("HH:mm"),
                    Index = info.Index,
                });
            }
            state.TodayCount = lessons.Count;
            foreach (var l in lessons)
            {
                var start = now.Date + TimeSpan.Parse(l.Start);
                var end = now.Date + TimeSpan.Parse(l.End);
                if (now >= start && now < end)
                {
                    state.InClass = true;
                    state.Subject = l.Subject;
                    state.Start = l.Start;
                    state.End = l.End;
                    state.TimeState = "onClass";
                    break;
                }
            }
            if (!state.InClass)
            {
                foreach (var l in lessons)
                {
                    var start = now.Date + TimeSpan.Parse(l.Start);
                    if (start <= now) continue;
                    state.Next = l.Subject;
                    state.NextStart = l.Start;
                    state.Preparing = (start - now).TotalMinutes <= 10;
                    state.TimeState = state.Preparing ? "prepareOnClass" : "breaking";
                    break;
                }
                if (state.Next == null) state.TimeState = "afterSchool";
            }
        }
        catch (Exception ex)
        {
            state.Error = ex.Message;
            BridgeLog.Write("计算课程状态失败：" + ex.Message);
        }
        return state;
    }

    private sealed class StateDto
    {
        public bool Ok { get; set; }
        public string Host { get; set; } = "ClassIsland";
        public string Version { get; set; } = "1.0.0";
        public string UpdatedAt { get; set; } = "";
        public bool InClass { get; set; }
        public bool Preparing { get; set; }
        public string Subject { get; set; } = "";
        public string ClassName { get; set; } = "";
        public string Start { get; set; } = "";
        public string End { get; set; } = "";
        public string? Next { get; set; }
        public string? NextStart { get; set; }
        public string TimeState { get; set; } = "none";
        public int TodayCount { get; set; }
        public string? Error { get; set; }
    }

    private sealed class LessonDto
    {
        public string Subject { get; set; } = "";
        public string Start { get; set; } = "";
        public string End { get; set; } = "";
        public int Index { get; set; }
    }
}
