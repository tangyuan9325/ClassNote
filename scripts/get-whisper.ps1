# 下载本地语音模型（whisper.cpp）运行时与模型文件。
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\get-whisper.ps1            # 默认 base-q5_1（60MB）
#   powershell -ExecutionPolicy Bypass -File scripts\get-whisper.ps1 -Model small-q5_1
#
# 可用模型（越大越准，中文课堂建议 small 及以上）：
#   tiny-q5_1 (32MB) | base-q5_1 (60MB) | small-q5_1 (190MB)
#   medium-q5_0 (539MB) | large-v3-turbo-q5_0 (574MB)
#
# 说明：程序本身也能下模型（设置 → 语音 → 下载），这条脚本是给
# 「要在离线机器上预置」或「想自己控制下载时点」的构建/运维场景用的。
param(
    [string]$Model = "base-q5_1",
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
    $keep = @("whisper-server.exe", "whisper-cli.exe", "whisper.dll", "ggml.dll", "ggml-base.dll", "SDL2.dll")
    foreach ($e in $zip.Entries) {
        $name = [System.IO.Path]::GetFileName($e.FullName)
        if (($keep -contains $name) -or ($name -like "ggml-cpu-*.dll")) {
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

# 3) 校验：逐字节比对官方 sha256，防止半截文件或镜像投毒。
$sha = @{
    "ggml-tiny-q5_1.bin"            = "818710568da3ca15689e31a743197b520007872ff9576237bda97bd1b469c3d7"
    "ggml-base-q5_1.bin"            = "422f1ae452ade6f30a004d7e5c6a43195e4433bc370bf23fac9cc591f01a8898"
    "ggml-small-q5_1.bin"           = "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb"
    "ggml-medium-q5_0.bin"          = "19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f"
    "ggml-large-v3-turbo-q5_0.bin"  = "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2"
}
if ($sha.ContainsKey($file)) {
    $got = (Get-FileHash -Path $dst -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $sha[$file]) {
        Remove-Item $dst -Force
        throw "校验失败：$file 的 sha256 与官方值不一致，已删除。请重试或换网络。"
    }
    Write-Host "    sha256 校验通过" -ForegroundColor DarkGray
}
Write-Host "完成：$dst" -ForegroundColor Green
Write-Host "提示：whisper 是 C 程序，读不懂中文路径。程序首次启动时会自动把模型部署到 C:\ProgramData\ClassNote\whisper。" -ForegroundColor DarkGray
