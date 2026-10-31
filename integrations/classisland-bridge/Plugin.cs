using ClassIsland.Core.Abstractions;
using ClassIsland.Core.Attributes;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;

namespace ClassNote.IslandBridge;

/// <summary>
/// ClassNote（课记）与 ClassIsland 的注入式联动插件。
/// 安装后由 ClassIsland 加载，向 ClassNote 提供精确的上课状态，并接收课堂提醒。
/// </summary>
[PluginEntrance]
public sealed class Plugin : PluginBase
{
    internal static string ConfigFolder = "";

    public override void Initialize(HostBuilderContext context, IServiceCollection services)
    {
        ConfigFolder = string.IsNullOrWhiteSpace(PluginConfigFolder)
            ? Path.Combine(Path.GetTempPath(), "ClassNote.IslandBridge")
            : PluginConfigFolder;
        try { Directory.CreateDirectory(ConfigFolder); } catch { /* ignore */ }
        BridgeLog.Write("插件初始化，配置目录：" + ConfigFolder);

        services.AddSingleton<BridgeService>();
        services.AddHostedService(sp => sp.GetRequiredService<BridgeService>());
    }
}

internal static class BridgeLog
{
    private static readonly object Gate = new();

    public static void Write(string message)
    {
        try
        {
            var dir = Plugin.ConfigFolder;
            if (string.IsNullOrEmpty(dir)) dir = Path.GetTempPath();
            var path = Path.Combine(dir, "bridge.log");
            lock (Gate)
            {
                File.AppendAllText(path, $"{DateTime.Now:yyyy-MM-dd HH:mm:ss} {message}{Environment.NewLine}");
            }
        }
        catch { /* 日志失败不能影响课堂 */ }
    }
}
