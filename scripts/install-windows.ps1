# Limoni Voice - Windows 1-Click Installer & Dependency Setup
# Installs Limoni Voice along with FFmpeg and MPV for full screenshare support.

[CmdletBinding()]
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\LimoniVoice"
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "   🍋 Limoni Voice - Windows Setup       " -ForegroundColor Yellow
Write-Host "==========================================" -ForegroundColor Cyan

$binDir = Join-Path $InstallDir "bin"
if (!(Test-Path $binDir)) {
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
}

# 1. Copy current binary if present in current directory
$currentExe = Join-Path $PSScriptRoot "limoni-voice.exe"
if (!(Test-Path $currentExe)) {
    $currentExe = Join-Path (Get-Location) "limoni-voice.exe"
}
if (Test-Path $currentExe) {
    Copy-Item $currentExe (Join-Path $InstallDir "limoni-voice.exe") -Force
    Write-Host "[+] limoni-voice.exe kopyalandi -> $InstallDir" -ForegroundColor Green
}

# 2. Check and Download FFmpeg if missing
$ffmpegExe = Join-Path $binDir "ffmpeg.exe"
if (!(Test-Path $ffmpegExe) -and !(Get-Command "ffmpeg.exe" -ErrorAction SilentlyContinue)) {
    Write-Host "[*] FFmpeg indiriliyor (Ekran yayini icin gerekli)..." -ForegroundColor Yellow
    $ffmpegZip = Join-Path $env:TEMP "ffmpeg-release-essentials.zip"
    $ffmpegUrl = "https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip"
    try {
        Invoke-WebRequest -Uri $ffmpegUrl -OutFile $ffmpegZip -UseBasicParsing
        Expand-Archive -Path $ffmpegZip -DestinationPath "$env:TEMP\ffmpeg_extracted" -Force
        $extractedFfmpeg = Get-ChildItem -Path "$env:TEMP\ffmpeg_extracted" -Recurse -Filter "ffmpeg.exe" | Select-Object -First 1
        if ($extractedFfmpeg) {
            Copy-Item $extractedFfmpeg.FullName $binDir -Force
            Write-Host "[+] ffmpeg.exe basariyla kuruldu!" -ForegroundColor Green
        }
        Remove-Item $ffmpegZip -Force -ErrorAction SilentlyContinue
        Remove-Item "$env:TEMP\ffmpeg_extracted" -Recurse -Force -ErrorAction SilentlyContinue
    } catch {
        Write-Host "[-] Otomatik indirme basarisiz, winget deneniyor..." -ForegroundColor DarkYellow
        winget install Gyan.FFmpeg --accept-source-agreements --accept-package-agreements
    }
} else {
    Write-Host "[+] FFmpeg zaten mevcut." -ForegroundColor Green
}

# 3. Check and Download MPV if missing
$mpvExe = Join-Path $binDir "mpv.exe"
if (!(Test-Path $mpvExe) -and !(Get-Command "mpv.exe" -ErrorAction SilentlyContinue)) {
    Write-Host "[*] MPV Player indiriliyor (Yayin izleyici icin gerekli)..." -ForegroundColor Yellow
    try {
        winget install mpv.mpv --accept-source-agreements --accept-package-agreements
        Write-Host "[+] MPV basariyla kuruldu!" -ForegroundColor Green
    } catch {
        Write-Host "[-] winget ile kurulamadi, lutfen https://mpv.io adresinden yukleyin." -ForegroundColor DarkYellow
    }
} else {
    Write-Host "[+] MPV zaten mevcut." -ForegroundColor Green
}

# 4. Add bin directory to User PATH
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$binDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
    Write-Host "[+] $binDir Kullanici PATH degiskenine eklendi." -ForegroundColor Green
}

# 5. Create Desktop Shortcut
$desktop = [Environment]::GetFolderPath("Desktop")
$shortcutPath = Join-Path $desktop "Limoni Voice.lnk"
$wscript = New-Object -ComObject WScript.Shell
$shortcut = $wscript.CreateShortcut($shortcutPath)
$target = Join-Path $InstallDir "limoni-voice.exe"
if (Test-Path $target) {
    $shortcut.TargetPath = $target
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "Limoni Voice - P2P Encrypted Voice & Screen Sharing"
    $shortcut.Save()
    Write-Host "[+] Masaustu kisayolu olusturuldu!" -ForegroundColor Green
}

Write-Host "`n==========================================" -ForegroundColor Cyan
Write-Host " [✓] Kurulum Tamamlandi! Limoni Voice Hazir." -ForegroundColor Green
Write-Host "==========================================" -ForegroundColor Cyan
