# 推送到 GitHub
# 用法：powershell -ExecutionPolicy Bypass -File push.ps1 -Repo https://github.com/<你的账号>/ClassNote.git
param(
    [Parameter(Mandatory = $true)][string]$Repo
)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $MyInvocation.MyCommand.Path)

if (-not (Test-Path ".git")) { throw "当前目录不是 git 仓库" }

$existing = git remote 2>$null
if ($existing -contains "origin") { git remote remove origin }
git remote add origin $Repo
git branch -M main
git push -u origin main
Write-Host "已推送：$Repo" -ForegroundColor Green
