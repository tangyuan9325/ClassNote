# Build ClassNote for Windows
# Usage: powershell -ExecutionPolicy Bypass -File build.ps1 [-Version 1.8.1]
param(
    [string]$Version = "1.8.1"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

Write-Host "[1/5] tidy modules" -ForegroundColor Cyan
go mod tidy

Write-Host "[2/5] build GUI binary" -ForegroundColor Cyan
$ldflags = "-H windowsgui -s -X main.version=$Version -X main.buildTime=$(Get-Date -Format 'yyyy-MM-dd')"
go build -trimpath -ldflags $ldflags -o "ClassNote.exe" .
if ($LASTEXITCODE -ne 0) { throw "build failed" }

Write-Host "[3/5] build debug binary" -ForegroundColor Cyan
go build -trimpath -ldflags "-X main.version=$Version-debug" -o "ClassNote-debug.exe" .
if ($LASTEXITCODE -ne 0) { throw "debug build failed" }

Write-Host "[4/5] stage package" -ForegroundColor Cyan
$stage = Join-Path $root "dist\ClassNote-$Version-windows-amd64"
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Copy-Item "ClassNote.exe" $stage
Copy-Item "ClassNote-debug.exe" $stage
Copy-Item "README.md","LICENSE","THIRD_PARTY_NOTICES.md" $stage
Copy-Item -Recurse "docs" $stage
# 本地语音模型（whisper.cpp）随包分发：开箱即用，无需安装
if (Test-Path "integrations\whisper") {
    Write-Host "  bundling whisper runtime + model (this is the big one)" -ForegroundColor DarkGray
    New-Item -ItemType Directory -Force -Path (Join-Path $stage "integrations\whisper\models") | Out-Null
    Copy-Item "integrations\whisper\*.exe" (Join-Path $stage "integrations\whisper") -Force
    Copy-Item "integrations\whisper\*.dll" (Join-Path $stage "integrations\whisper") -Force
    Copy-Item "integrations\whisper\models\*.bin" (Join-Path $stage "integrations\whisper\models") -Force
}
New-Item -ItemType Directory -Force -Path (Join-Path $stage "plugins") | Out-Null
if (Test-Path "plugins\ClassNote.Bridge.dll") {
    Copy-Item "plugins\ClassNote.Bridge.dll" (Join-Path $stage "plugins")
    Write-Host "  bundled ICC bridge plugin" -ForegroundColor DarkGray
}
Set-Content -Path (Join-Path $stage "plugins\README.txt") -Encoding utf8 -Value @"
ClassNote.Bridge.dll 是 InkCanvasForClass 的联动插件。
程序的“环境检查 -> 注入插件”会把它复制到 ICC 的 Plugins 目录并自动启用。

The bridge targets .NET Framework 4.7.2, matching InkCanvasForClass 1.7.x.
If your ICC version uses a different plugin ABI, rebuild from integrations/icc-bridge.
Slide detection works even without the plugin.
"@

Write-Host "[5/5] zip" -ForegroundColor Cyan
$zip = Join-Path $root "dist\ClassNote-$Version-windows-amd64.zip"
if (Test-Path $zip) { Remove-Item -Force $zip }
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zip -CompressionLevel Optimal

Write-Host ""
Write-Host "Done: $zip" -ForegroundColor Green
Write-Host "Size: $([math]::Round((Get-Item $zip).Length / 1MB, 1)) MB"
