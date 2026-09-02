# Limoni Voice - Windows 1-Click Installer & Dependency Setup
# Installs Limoni Voice along with FFmpeg, MPV, 3D assets, and custom icon.

[CmdletBinding()]
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\LimoniVoice"
)

$ErrorActionPreference = "Stop"

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

# 2. Download or copy icon.ico and microphone.obj
$iconPath = Join-Path $InstallDir "icon.ico"
$micPath = Join-Path $InstallDir "microphone.obj"

try {
    if (!(Test-Path $iconPath)) {
        Invoke-WebRequest -Uri "https://raw.githubusercontent.com/thebanri/limoni-voice/main/assets/icon.ico" -OutFile $iconPath -UseBasicParsing
    }
    if (!(Test-Path $micPath)) {
        Invoke-WebRequest -Uri "https://raw.githubusercontent.com/thebanri/limoni-voice/main/microphone.obj" -OutFile $micPath -UseBasicParsing
    }
} catch {
    Write-Host "[-] Warning downloading assets: $_" -ForegroundColor DarkYellow
}

# 3. Check and Download FFmpeg if missing
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
            winget install Gyan.FFmpeg --accept-source-agreements --accept-package-agreements
        } catch {}
    }
} else {
    Write-Host "[+] FFmpeg is already installed." -ForegroundColor Green
}

# 4. Check and Download MPV if missing
$mpvExe = Join-Path $binDir "mpv.exe"
if (!(Test-Path $mpvExe) -and !(Get-Command "mpv.exe" -ErrorAction SilentlyContinue)) {
    Write-Host "[*] Checking MPV Player (required for screen stream viewing)..." -ForegroundColor Yellow
    $mpvInstalled = $false

    # Try winget first
    try {
        winget install --id mpv.mpv -e --accept-source-agreements --accept-package-agreements | Out-Null
        $mpvInstalled = $true
    } catch {
        try {
            winget install --id shinchiro.mpv -e --accept-source-agreements --accept-package-agreements | Out-Null
            $mpvInstalled = $true
        } catch {
            try {
                winget install --id mpv.net -e --accept-source-agreements --accept-package-agreements | Out-Null
                $mpvInstalled = $true
            } catch {}
        }
    }

    # Search for winget extracted packages or existing installations and copy mpv.exe to binDir
    $searchLocations = @(
        (Join-Path $env:LOCALAPPDATA "Microsoft\WinGet\Packages"),
        (Join-Path $env:LOCALAPPDATA "Microsoft\WinGet\Links"),
        (Join-Path $env:LOCALAPPDATA "Programs\mpv"),
        (Join-Path $env:LOCALAPPDATA "Programs\mpv.net"),
        (Join-Path $env:ProgramFiles "mpv"),
        (Join-Path $env:ProgramFiles "mpv.net"),
        (Join-Path $env:USERPROFILE "scoop\shims"),
        (Join-Path $env:USERPROFILE "scoop\apps\mpv\current")
    )

    foreach ($loc in $searchLocations) {
        if (Test-Path $loc) {
            $found = Get-ChildItem -Path $loc -Recurse -Filter "mpv.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($found -and (Test-Path $found.FullName)) {
                Copy-Item $found.FullName $binDir -Force -ErrorAction SilentlyContinue
                Write-Host "[+] mpv.exe copied from $($found.FullName) to $binDir" -ForegroundColor Green
                break
            }
        }
    }

    if (Test-Path $mpvExe) {
        Write-Host "[+] MPV installed successfully into $binDir!" -ForegroundColor Green
    } else {
        Write-Host "[*] Downloading portable MPV Player directly..." -ForegroundColor Yellow
        $mpvZip = Join-Path $env:TEMP "mpv_build.zip"
        $mpvUrls = @(
            "https://github.com/thebanri/limoni-voice/releases/download/v1.0.0/mpv.exe",
            "https://sourceforge.net/projects/mpv-player-windows/files/64bit/mpv-x86_64-20240901-git-1beea03.7z/download"
        )
        foreach ($url in $mpvUrls) {
            try {
                if ($url.EndsWith(".exe")) {
                    Invoke-WebRequest -Uri $url -OutFile $mpvExe -UseBasicParsing
                    if (Test-Path $mpvExe) {
                        Write-Host "[+] mpv.exe downloaded directly to $binDir!" -ForegroundColor Green
                        break
                    }
                }
            } catch {}
        }
    }
} else {
    Write-Host "[+] MPV is already installed." -ForegroundColor Green
}

# 5. Add bin directory to User PATH and current session PATH
$env:PATH = "$binDir;$env:PATH"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ([string]::IsNullOrWhiteSpace($userPath)) {
    [Environment]::SetEnvironmentVariable("Path", $binDir, "User")
} elseif ($userPath -notlike "*$binDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$binDir;$userPath", "User")
    Write-Host "[+] $binDir added to User PATH environment variable." -ForegroundColor Green
}

# 6. Create Desktop Shortcut with custom Icon
$desktop = [Environment]::GetFolderPath("Desktop")
if (![string]::IsNullOrWhiteSpace($desktop) -and (Test-Path $desktop)) {
    try {
        $shortcutPath = Join-Path $desktop "Limoni Voice.lnk"
        $wscript = New-Object -ComObject WScript.Shell
        $shortcut = $wscript.CreateShortcut($shortcutPath)
        if (Test-Path $targetExe) {
            $shortcut.TargetPath = $targetExe
            $shortcut.WorkingDirectory = $InstallDir
            if (Test-Path $iconPath) {
                $shortcut.IconLocation = "$iconPath,0"
            }
            $shortcut.Description = "Limoni Voice - P2P Encrypted Voice & Screen Sharing"
            $shortcut.Save()
            Write-Host "[+] Desktop shortcut created (with custom icon)!" -ForegroundColor Green
        }
    } catch {
        Write-Host "[-] Could not create desktop shortcut: $_" -ForegroundColor DarkYellow
    }
}

# Unblock files so Windows Defender / SmartScreen never interferes
try {
    Unblock-File -Path "$InstallDir\*" -ErrorAction SilentlyContinue
    Unblock-File -Path "$binDir\*" -ErrorAction SilentlyContinue
} catch {}

Write-Host "`n==========================================" -ForegroundColor Cyan
Write-Host " [✓] Installation Complete! Limoni Voice is ready." -ForegroundColor Green
Write-Host "==========================================" -ForegroundColor Cyan
