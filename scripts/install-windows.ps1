# Limoni Voice - Windows 1-Click Installer & Dependency Setup
# Installs Limoni Voice along with FFmpeg, MPV, 3D assets, and custom icon.

[CmdletBinding()]
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\LimoniVoice"
)

$ErrorActionPreference = "Stop"
# Windows PowerShell 5.1 draws a progress bar per received chunk, which makes large
# downloads (FFmpeg, MPV) many times slower.
$ProgressPreference = "SilentlyContinue"

# Enable TLS 1.2 for legacy Windows PowerShell 5.1 environments
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "   🍋 Limoni Voice - Windows Setup       " -ForegroundColor Yellow
Write-Host "==========================================" -ForegroundColor Cyan

if ([string]::IsNullOrWhiteSpace($InstallDir) -or $InstallDir -eq "\LimoniVoice") {
    $InstallDir = Join-Path $env:USERPROFILE "AppData\Local\LimoniVoice"
}

$binDir = Join-Path $InstallDir "bin"
if (!(Test-Path $binDir)) {
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
}

$repoUrl = "https://github.com/thebanri/limoni-voice"

# 1. Download or copy limoni-voice.exe
$targetExe = Join-Path $InstallDir "limoni-voice.exe"
$currentExe = $null

# Safely check if script is running from a local directory (when not piped to iex)
if (![string]::IsNullOrWhiteSpace($PSScriptRoot)) {
    $testLocal = Join-Path $PSScriptRoot "limoni-voice.exe"
    if (Test-Path $testLocal) {
        $currentExe = $testLocal
    }
}

# Safely check current working directory
if (!$currentExe) {
    $loc = (Get-Location).Path
    if (![string]::IsNullOrWhiteSpace($loc)) {
        $testCwd = Join-Path $loc "limoni-voice.exe"
        if (Test-Path $testCwd) {
            $currentExe = $testCwd
        }
    }
}

if ($currentExe -and (Test-Path $currentExe)) {
    Copy-Item $currentExe $targetExe -Force
    Write-Host "[+] limoni-voice.exe copied from local directory." -ForegroundColor Green
} else {
    Write-Host "[*] Downloading limoni-voice.exe from GitHub Releases..." -ForegroundColor Yellow
    $arch = "windows_amd64"
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
        $arch = "windows_arm64"
    }
    $releaseUrl = "https://github.com/thebanri/limoni-voice/releases/latest/download/limoni-voice_${arch}.exe"
    $fallbackUrl = "https://github.com/thebanri/limoni-voice/releases/latest/download/limoni-voice.exe"
    $success = $false
    try {
        Invoke-WebRequest -Uri $releaseUrl -OutFile $targetExe -UseBasicParsing
        $success = $true
        Write-Host "[+] limoni-voice.exe downloaded successfully." -ForegroundColor Green
    } catch {
        try {
            Invoke-WebRequest -Uri $fallbackUrl -OutFile $targetExe -UseBasicParsing
            $success = $true
            Write-Host "[+] limoni-voice.exe downloaded successfully." -ForegroundColor Green
        } catch {
            Write-Host "[-] Failed to download limoni-voice.exe: $_" -ForegroundColor DarkYellow
        }
    }
}

# 2. Download icon.ico (the 3D microphone model is embedded in the app)
$iconPath = Join-Path $InstallDir "icon.ico"

try {
    if (!(Test-Path $iconPath)) {
        Invoke-WebRequest -Uri "https://raw.githubusercontent.com/thebanri/limoni-voice/main/assets/icon.ico" -OutFile $iconPath -UseBasicParsing
    }
} catch {
    Write-Host "[-] Warning downloading assets: $_" -ForegroundColor DarkYellow
}

# 3. Shortcuts and invite links come before the FFmpeg and MPV downloads: winget can take
# minutes, and a window closed during it must not leave the app without a shortcut.
try {
    Unblock-File -Path $targetExe -ErrorAction SilentlyContinue
} catch {}

if (Test-Path $targetExe) {
    $shortcutFolders = [ordered]@{
        "Desktop"    = [Environment]::GetFolderPath("Desktop")
        "Start Menu" = [Environment]::GetFolderPath("Programs")
    }
    if ([string]::IsNullOrWhiteSpace($shortcutFolders["Desktop"])) {
        $shortcutFolders["Desktop"] = Join-Path $env:USERPROFILE "Desktop"
    }
    foreach ($name in $shortcutFolders.Keys) {
        $folder = $shortcutFolders[$name]
        try {
            if ([string]::IsNullOrWhiteSpace($folder)) {
                throw "folder not found"
            }
            if (!(Test-Path $folder)) {
                New-Item -ItemType Directory -Force -Path $folder | Out-Null
            }
            $shortcutPath = Join-Path $folder "Limoni Voice.lnk"
            $wscript = New-Object -ComObject WScript.Shell
            $shortcut = $wscript.CreateShortcut($shortcutPath)
            $shortcut.TargetPath = $targetExe
            $shortcut.WorkingDirectory = $InstallDir
            if (Test-Path $iconPath) {
                $shortcut.IconLocation = "$iconPath,0"
            }
            $shortcut.Description = "Limoni Voice - P2P Encrypted Voice & Screen Sharing"
            $shortcut.Save()
            Write-Host "[+] $name shortcut created: $shortcutPath" -ForegroundColor Green
        } catch {
            Write-Host "[-] Could not create the $name shortcut: $_" -ForegroundColor DarkYellow
        }
    }
} else {
    Write-Host "[!] Shortcuts skipped: limoni-voice.exe is missing (an antivirus may have removed it)." -ForegroundColor Red
}

# 4. Open limoni://join/<room key> invite links with Limoni Voice (current user, no admin)
try {
    $scheme = "HKCU:\Software\Classes\limoni"
    New-Item -Path "$scheme\shell\open\command" -Force | Out-Null
    New-Item -Path "$scheme\DefaultIcon" -Force | Out-Null
    Set-ItemProperty -Path $scheme -Name "(default)" -Value "URL:Limoni Voice invite"
    Set-ItemProperty -Path $scheme -Name "URL Protocol" -Value ""
    Set-ItemProperty -Path "$scheme\DefaultIcon" -Name "(default)" -Value $iconPath
    Set-ItemProperty -Path "$scheme\shell\open\command" -Name "(default)" -Value "`"$targetExe`" `"%1`""
    Write-Host "[+] limoni:// invite links now open Limoni Voice" -ForegroundColor Green
} catch {
    Write-Host "[-] Could not register limoni:// invite links: $_" -ForegroundColor DarkYellow
}

# 5. Check and Download FFmpeg if missing
$ffmpegExe = Join-Path $binDir "ffmpeg.exe"
if (!(Test-Path $ffmpegExe) -and !(Get-Command "ffmpeg.exe" -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Downloading FFmpeg (required for screen broadcasting)..." -ForegroundColor Yellow
    $ffmpegZip = Join-Path $env:TEMP "ffmpeg-release-essentials.zip"
    $ffmpegUrl = "https://github.com/GyanD/codexffmpeg/releases/download/7.1/ffmpeg-7.1-essentials_build.zip"
    try {
        Invoke-WebRequest -Uri $ffmpegUrl -OutFile $ffmpegZip -UseBasicParsing
        Expand-Archive -Path $ffmpegZip -DestinationPath "$env:TEMP\ffmpeg_extracted" -Force
        $extractedFfmpeg = Get-ChildItem -Path "$env:TEMP\ffmpeg_extracted" -Recurse -Filter "ffmpeg.exe" | Select-Object -First 1
        if ($extractedFfmpeg) {
            Copy-Item $extractedFfmpeg.FullName $binDir -Force
            Write-Host "[+] ffmpeg.exe installed successfully!" -ForegroundColor Green
        }
        Remove-Item $ffmpegZip -Force -ErrorAction SilentlyContinue
        Remove-Item "$env:TEMP\ffmpeg_extracted" -Recurse -Force -ErrorAction SilentlyContinue
    } catch {
        Write-Host "[-] Automatic download failed, attempting winget..." -ForegroundColor DarkYellow
        try {
            winget install -e --id Gyan.FFmpeg --accept-source-agreements --accept-package-agreements
        } catch {}
    }
} else {
    Write-Host "[+] FFmpeg is already installed." -ForegroundColor Green
}

# 6. MPV: the official build, mpv.exe plus the Vulkan loader it needs, checked against its SHA-256.
# (The winget package needs administrator rights and takes over media file types: last resort.)
$mpvExe = Join-Path $binDir "mpv.exe"
if (!(Test-Path $mpvExe) -and !(Get-Command "mpv.exe" -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Downloading MPV Player (required for screen stream viewing)..." -ForegroundColor Yellow
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
        $mpvUrl = "https://github.com/mpv-player/mpv/releases/download/v0.41.0/mpv-v0.41.0-aarch64-pc-windows-msvc.zip"
        $mpvHash = "a822abeffd0ac88951f4084f3425f949842aa17d616f880637ebe9041e482e97"
    } else {
        $mpvUrl = "https://github.com/mpv-player/mpv/releases/download/v0.41.0/mpv-v0.41.0-x86_64-pc-windows-msvc.zip"
        $mpvHash = "4e197f729f5071c6772f35fffd96e0f36e3e8a044bd9479b136bb09b7c6a80ff"
    }
    $mpvZip = Join-Path $env:TEMP "limoni_mpv_setup.zip"
    try {
        Invoke-WebRequest -Uri $mpvUrl -OutFile $mpvZip -UseBasicParsing
        if ((Get-FileHash -Path $mpvZip -Algorithm SHA256).Hash -ne $mpvHash) {
            throw "download corrupted (SHA-256 mismatch)"
        }
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $zip = [System.IO.Compression.ZipFile]::OpenRead($mpvZip)
        try {
            foreach ($name in @("mpv.exe", "vulkan-1.dll")) {
                $entry = $zip.Entries | Where-Object { $_.Name -eq $name } | Select-Object -First 1
                if (!$entry) { throw "$name not found in the archive" }
                [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, (Join-Path $binDir $name), $true)
            }
        } finally {
            $zip.Dispose()
        }
        Write-Host "[+] MPV installed into $binDir" -ForegroundColor Green
    } catch {
        Write-Host "[-] MPV download failed: $_" -ForegroundColor DarkYellow
        Write-Host "[*] Trying winget (shinchiro.mpv); Windows may ask for permission..." -ForegroundColor Yellow
        try {
            winget install -e --id shinchiro.mpv --accept-source-agreements --accept-package-agreements
        } catch {
            Write-Host "[-] winget is not available: $_" -ForegroundColor DarkYellow
        }
    } finally {
        Remove-Item $mpvZip -Force -ErrorAction SilentlyContinue
    }
} else {
    Write-Host "[+] MPV is already installed." -ForegroundColor Green
}

# Unblock files so Windows Defender / SmartScreen never interferes
try {
    Unblock-File -Path "$InstallDir\*" -ErrorAction SilentlyContinue
    Unblock-File -Path "$binDir\*" -ErrorAction SilentlyContinue
} catch {}

# 7. Add bin directory to User PATH and current session PATH
$env:PATH = "$binDir;$env:PATH"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ([string]::IsNullOrWhiteSpace($userPath)) {
    [Environment]::SetEnvironmentVariable("Path", $binDir, "User")
} elseif ($userPath -notlike "*$binDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
    Write-Host "[+] $binDir added to User PATH environment variable." -ForegroundColor Green
}

Write-Host "`n==========================================" -ForegroundColor Cyan
Write-Host " [✓] Installation Complete! Limoni Voice is ready." -ForegroundColor Green
Write-Host "==========================================" -ForegroundColor Cyan
