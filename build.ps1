# Builds the Jellyfin RPC mod: version.dll (proxy) + jellyfinrpc_core.dll (engine).
#   powershell -ExecutionPolicy Bypass -File build.ps1 [-Version 1.0.0]
# Needs Go and llvm-mingw (paths below, or on PATH).
param([string]$Version = "dev")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$tools = "C:\Dev\tools"
$llvm = Get-ChildItem $tools -Directory -Filter "llvm-mingw-*" -ErrorAction SilentlyContinue | Select-Object -First 1
if (Test-Path "$tools\go\bin") { $env:PATH = "$tools\go\bin;$env:PATH" }
if ($llvm) { $env:PATH = "$($llvm.FullName)\bin;$env:PATH" }

New-Item -ItemType Directory -Force dist | Out-Null
if (-not (Test-Path go.sum)) { $env:GOOS = ""; go mod tidy; if ($LASTEXITCODE -ne 0) { throw "go mod tidy failed" } }
$env:CGO_ENABLED = "1"; $env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CC = "x86_64-w64-mingw32-clang"

Write-Host "building jellyfinrpc_core.dll"
go build -trimpath -buildmode=c-shared -ldflags "-s -w -X main.version=$Version" -o dist\jellyfinrpc_core.dll .\core
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

Write-Host "building version.dll"
x86_64-w64-mingw32-clang -O2 -shared -o dist\version.dll proxy\proxy.c proxy\version.def
if ($LASTEXITCODE -ne 0) { throw "proxy build failed" }

Remove-Item -ErrorAction SilentlyContinue dist\jellyfinrpc_core.h
Copy-Item README.md, LICENSE dist\
Get-FileHash dist\*.dll -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLower())  $(Split-Path $_.Path -Leaf)" } | Set-Content dist\SHA256SUMS.txt
Get-ChildItem dist
