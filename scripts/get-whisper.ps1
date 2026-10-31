# 下载本地语音模型（whisper.cpp）运行时与模型文件。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\get-whisper.ps1            # 默认 small-q5_1（约 182MB）
#   powershell -ExecutionPolicy Bypass -File scripts\get-whisper.ps1 -Model base-q5_1
#
# 可用模型（越大越准，中文建议 small 及以上）：
#   tiny-q5_1 (32MB) | base-q5_1 (57MB) | small-q5_1 (182MB) | medium-q5_1 (514MB) | large-v3-turbo-q5_1 (547MB)
param(
    [string]$Model = "small-q5_1",
    [string]$OutDir = ""
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
if ($OutDir -eq "") { $OutDir = Join-Path $root "integrations\whisper" }
$models = Join-Path $OutDir "models"
New-Item -ItemType Directory -Force -Path $models | Out-Null

# 1) 二进制（约 4MB）
$bin = Join-Path $OutDir "whisper-server.exe"
if (-not (Test-Path $bin)) {
    Write-Host "[1/2] 下载 whisper.cpp Windows 二进制..." -ForegroundColor Cyan
    $tmp = Join-Path $env:TEMP "whisper-bin-x64.zip"
    Invoke-WebRequest -Uri "https://github.com/ggml-org/whisper.cpp/releases/download/v1.9.2/whisper-bin-x64.zip" -OutFile $tmp
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($tmp)
    $keep = @("whisper-server.exe", "whisper-cli.exe", "whisper.dll", "ggml.dll", "ggml-base.dll", "ggml-cpu-x64.dll", "SDL2.dll")
    foreach ($e in $zip.Entries) {
        $name = [System.IO.Path]::GetFileName($e.FullName)
        if ($keep -contains $name) {
            $dst = Join-Path $OutDir $name
            [System.IO.Compression.ZipFileExtensions]::ExtractToFile($e, $dst, $true)
            Write-Host "    $name"
        }
    }
    $zip.Dispose()
    Remove-Item $tmp -Force
} else {
    Write-Host "[1/2] 二进制已存在，跳过" -ForegroundColor DarkGray
}

# 2) 模型
$file = "ggml-$Model.bin"
$dst = Join-Path $models $file
if (Test-Path $dst) {
    Write-Host "[2/2] 模型 $file 已存在，跳过" -ForegroundColor DarkGray
    return
}
Write-Host "[2/2] 下载模型 $file（可能需要几分钟）..." -ForegroundColor Cyan
# 优先走国内镜像，失败再退回官方地址
$urls = @(
    "https://hf-mirror.com/ggerganov/whisper.cpp/resolve/main/$file",
    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/$file"
)
$ok = $false
foreach ($u in $urls) {
    try {
        Invoke-WebRequest -Uri $u -OutFile $dst
        $ok = $true
        break
    } catch {
        Write-Host "    失败：$u" -ForegroundColor DarkGray
    }
}
if (-not $ok) { throw "模型下载失败，请手动把 $file 放到 $models" }
Write-Host "完成：$dst" -ForegroundColor Green
Write-Host "提示：whisper 是 C 程序，读不懂中文路径。程序首次启动时会自动把模型部署到 C:\ProgramData\ClassNote\whisper。" -ForegroundColor DarkGray
