# Agent Mask - check and install what it needs (ffmpeg, Tesseract OCR), asking first.
# Uses winget (Windows 10/11).
$ErrorActionPreference = 'Continue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$app = Join-Path $PSScriptRoot 'agent-mask.exe'
function Refresh-Path { $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User') }
function Has($cmd) { [bool](Get-Command $cmd -ErrorAction SilentlyContinue) }
function Ask($q) { $a = Read-Host "$q (Y/N)"; return $a -match '^(y|c|yes|co)' }

Write-Host ''
Write-Host '=== Agent Mask: components ===' -ForegroundColor Cyan
Refresh-Path
$hasWinget = Has 'winget'
if (-not (Has 'ffmpeg')) {
  Write-Host 'ffmpeg is missing (reads and writes the videos).' -ForegroundColor Yellow
  if ($hasWinget -and (Ask 'Install ffmpeg with winget (Gyan.FFmpeg)?')) {
    winget install --id Gyan.FFmpeg -e --accept-source-agreements --accept-package-agreements
    Refresh-Path
  }
} else { Write-Host 'OK  ffmpeg' -ForegroundColor Green }
$tessDir = 'C:\Program Files\Tesseract-OCR'
if (-not (Has 'tesseract') -and -not (Test-Path "$tessDir\tesseract.exe")) {
  Write-Host 'Tesseract OCR is missing (reads the text to find what to hide).' -ForegroundColor Yellow
  if ($hasWinget -and (Ask 'Install Tesseract with winget (UB-Mannheim.TesseractOCR)?')) {
    winget install --id UB-Mannheim.TesseractOCR -e --accept-source-agreements --accept-package-agreements
  }
}
if ((Test-Path "$tessDir\tesseract.exe") -and -not (Has 'tesseract')) {
  & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'path.ps1') -Dir $tessDir -Add
  Refresh-Path
}
if (Has 'tesseract') { Write-Host 'OK  tesseract' -ForegroundColor Green }
$vie = "$tessDir\tessdata\vie.traineddata"
if ((Test-Path "$tessDir\tessdata") -and -not (Test-Path $vie)) {
  if (Ask 'Download Vietnamese for the OCR (vie.traineddata, needs admin approval)?') {
    $url = 'https://github.com/tesseract-ocr/tessdata_fast/raw/main/vie.traineddata'
    Start-Process powershell -Verb RunAs -Wait -ArgumentList '-NoProfile', '-Command', "Invoke-WebRequest -UseBasicParsing -Uri '$url' -OutFile '$vie'"
  }
}
Write-Host ''
& $app doctor
Write-Host ''
Write-Host '=== Connect an agent (MCP) ===' -ForegroundColor Cyan
Write-Host "Claude Code:  claude mcp add agent-mask -- `"$app`" mcp"
Write-Host "Codex:        codex mcp add agent-mask -- `"$app`" mcp"
Write-Host "Cursor/other: stdio server, command = `"$app`", args = [`"mcp`"]"
Write-Host ''
Read-Host 'Press Enter to close'
