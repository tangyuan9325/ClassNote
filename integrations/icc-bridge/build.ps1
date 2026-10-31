# 编译 InkCanvasForClass 联动插件
# 用法：powershell -ExecutionPolicy Bypass -File build.ps1 -IccDir "E:\...\InkCanvasForClass.CE.x.y.z"
param(
    [Parameter(Mandatory = $true)][string]$IccDir
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $MyInvocation.MyCommand.Path)

if (-not (Test-Path (Join-Path $IccDir "InkCanvasForClass.exe"))) {
    throw "目录里没有 InkCanvasForClass.exe: $IccDir"
}
if (-not (Get-Command dotnet -ErrorAction SilentlyContinue)) {
    throw "需要 .NET SDK（dotnet build）。ICC 1.7.x 是 .NET Framework 4.7.2，SDK 会自动下载引用程序集。"
}

dotnet build ClassNote.Bridge.csproj -c Release -p:IccDir="$IccDir" -v minimal

$dll = Join-Path $PWD "bin\Release\ClassNote.Bridge.dll"
if (-not (Test-Path $dll)) { throw "编译产物不存在: $dll" }

$repoPlugins = Join-Path $PWD "..\..\plugins"
New-Item -ItemType Directory -Force -Path $repoPlugins | Out-Null
Copy-Item $dll $repoPlugins -Force
Write-Host ""
Write-Host "已生成: $dll" -ForegroundColor Green
Write-Host "已复制到: $repoPlugins" -ForegroundColor Green
Write-Host "安装: 复制到 <ICC>\Plugins\，并在 ClassNote 里点击“注入插件”以自动启用。" -ForegroundColor Cyan
