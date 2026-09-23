import Foundation
import Darwin
import ScreenCaptureKit
import CoreMedia
import CoreVideo
import CoreAudio
import CoreGraphics

_ = Darwin.signal(SIGPIPE, SIG_IGN)

let logFilePath = (NSTemporaryDirectory() as NSString).appendingPathComponent("limoni_mac_sckit.log")
func logToFile(_ msg: String) {
    let line = "[\(Date())] \(msg)\n"
    if let data = line.data(using: .utf8) {
        if let handle = FileHandle(forWritingAtPath: logFilePath) {
            handle.seekToEndOfFile()
            handle.write(data)
            handle.closeFile()
        } else {
            try? data.write(to: URL(fileURLWithPath: logFilePath))
        }
    }
    fputs(line, stderr)
}

func writeAll(fd: Int32, buffer: UnsafeRawPointer, count: Int) -> Bool {
    var written = 0
    while written < count {
        let n = write(fd, buffer.advanced(by: written), count - written)
        if n <= 0 {
            if errno == EINTR { continue }
            return false
        }
        written += n
    }
    return true
}

// checkScreenPermission reports a missing Screen Recording permission on a line Limoni Voice
// recognizes, and asks macOS to show its permission prompt.
func checkScreenPermission(_ out: UnsafeMutablePointer<FILE>) {
    if !CGPreflightScreenCaptureAccess() {
        fputs("PERMISSION|screen\n", out)
        fflush(out)
        _ = CGRequestScreenCaptureAccess()
    }
}

// pickDisplay returns the display to capture: the one asked for, else the one showing most of
// the shared window, else the main display.
@available(macOS 12.3, *)
func pickDisplay(_ displays: [SCDisplay], displayID: CGDirectDisplayID?, window: SCWindow?) -> SCDisplay? {
    if let id = displayID, let d = displays.first(where: { $0.displayID == id }) {
        return d
    }
    if let w = window {
        var best: SCDisplay? = nil
        var bestArea: CGFloat = 0
        for d in displays {
            let overlap = d.frame.intersection(w.frame)
            let area: CGFloat = overlap.isNull ? 0 : overlap.width * overlap.height
            if area > bestArea {
                best = d
                bestArea = area
            }
        }
        if let b = best {
            return b
        }
    }
    let mainID = CGMainDisplayID()
    return displays.first(where: { $0.displayID == mainID }) ?? displays.first
}

@available(macOS 12.3, *)
class ScreenRecorder: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    var audioStream: SCStream? // app-only audio when sharing one window
    var isRunning = false
    var frameCount = 0

    // System audio (macOS 13+): 48 kHz stereo float32, interleaved, written to fd 3.
    let audioFD: Int32 = 3
    var audioEnabled = false
    let audioQueue = DispatchQueue(label: "screen.audio.queue", qos: .userInteractive)

    func addAudioOutput(_ stream: SCStream, _ captureAudio: Bool) {
        guard captureAudio else { return }
        if #available(macOS 13.0, *) {
            do {
                try stream.addStreamOutput(self, type: .audio, sampleHandlerQueue: audioQueue)
                audioEnabled = true
                logToFile("[AUDIO] System audio capture enabled")
            } catch {
                logToFile("[AUDIO] System audio unavailable: \(error)")
            }
        } else {
            logToFile("[AUDIO] System audio capture requires macOS 13+")
        }
    }

    func handleAudio(_ sampleBuffer: CMSampleBuffer) {
        guard audioEnabled, let desc = sampleBuffer.formatDescription?.audioStreamBasicDescription else { return }
        let channels = Int(desc.mChannelsPerFrame)
        let isFloat = (desc.mFormatFlags & kAudioFormatFlagIsFloat) != 0
        let nonInterleaved = (desc.mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0
        guard isFloat, desc.mBitsPerChannel == 32, channels >= 1 else { return }
        let frames = sampleBuffer.numSamples
        guard frames > 0 else { return }
        do {
            try sampleBuffer.withAudioBufferList { abl, _ in
                var out = [Float32](repeating: 0, count: frames * 2)
                if nonInterleaved {
                    guard abl.count >= 1, let l = abl[0].mData?.assumingMemoryBound(to: Float32.self) else { return }
                    let r = abl.count >= 2 ? abl[1].mData?.assumingMemoryBound(to: Float32.self) : nil
                    for i in 0..<frames {
                        out[2 * i] = l[i]
                        out[2 * i + 1] = r?[i] ?? l[i]
                    }
                } else {
                    guard abl.count >= 1, let p = abl[0].mData?.assumingMemoryBound(to: Float32.self) else { return }
                    for i in 0..<frames {
                        out[2 * i] = p[i * channels]
                        out[2 * i + 1] = channels > 1 ? p[i * channels + 1] : p[i * channels]
                    }
                }
                out.withUnsafeBytes { raw in
                    if let base = raw.baseAddress, !writeAll(fd: audioFD, buffer: base, count: raw.count) {
                        logToFile("[AUDIO] Audio pipe closed, disabling system audio")
                        self.audioEnabled = false
                    }
                }
            }
        } catch {
            logToFile("[AUDIO] Could not read audio buffer: \(error)")
        }
    }

    func start(fps: Int = 60, width: Int = 1920, height: Int = 1080, targetWindowID: CGWindowID? = nil, targetDisplayID: CGDirectDisplayID? = nil, captureAudio: Bool = false) async {
        logToFile("[START] Initializing ScreenCaptureKit capture: \(width)x\(height) @ \(fps) FPS, targetWindowID=\(String(describing: targetWindowID))")
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
            let windowTarget = targetWindowID.flatMap { id in content.windows.first(where: { $0.windowID == id }) }
            guard let display = pickDisplay(content.displays, displayID: targetDisplayID, window: windowTarget) else {
                logToFile("[ERR] No display found in SCShareableContent")
                exit(1)
            }
            logToFile("[INFO] Found \(content.displays.count) displays and \(content.windows.count) windows")

            var filter: SCContentFilter
            // Sharing one window shares only its application's sound, captured by a second
            // stream filtered to that application; the video stream then carries no audio.
            var audioApp: SCRunningApplication? = nil
            if let targetWin = windowTarget {
                logToFile("[INFO] Target window found: '\(targetWin.title ?? "")' (app: '\(targetWin.owningApplication?.applicationName ?? "")', id: \(targetWin.windowID), frame: \(targetWin.frame), display: \(display.displayID))")
                // Use display-including window filter: guarantees exact fixed canvas dimensions for FFmpeg
                filter = SCContentFilter(display: display, including: [targetWin])
                if captureAudio {
                    audioApp = targetWin.owningApplication
                }
            } else {
                logToFile("[INFO] Using full display capture (Display ID: \(display.displayID), resolution: \(display.width)x\(display.height))")
                // Exclude Limoni Voice itself (the parent process) so voice chat is not re-shared.
                let parentPID = getppid()
                let selfApps = content.applications.filter { $0.processID == parentPID }
                filter = SCContentFilter(display: display, excludingApplications: selfApps, exceptingWindows: [])
            }

            let config = SCStreamConfiguration()
            config.width = width
            config.height = height
            config.scalesToFit = true
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 8
            if #available(macOS 13.0, *), captureAudio, audioApp == nil {
                config.capturesAudio = true
                config.sampleRate = 48000
                config.channelCount = 2
                config.excludesCurrentProcessAudio = true
            }

            let stream = SCStream(filter: filter, configuration: config, delegate: self)
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
            addAudioOutput(stream, captureAudio && audioApp == nil)

            do {
                try await stream.startCapture()
                self.stream = stream
                self.isRunning = true
                logToFile("[OK] Stream started successfully at \(width)x\(height) @ \(fps) FPS")
                if let app = audioApp {
                    if #available(macOS 13.0, *) {
                        await startAppAudio(display: display, app: app)
                    } else {
                        logToFile("[AUDIO] System audio capture requires macOS 13+")
                    }
                }
            } catch {
                logToFile("[WARN] Filter startCapture failed (\(error)), trying fallback full display filter...")
                let fallbackFilter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
                let fallbackStream = SCStream(filter: fallbackFilter, configuration: config, delegate: self)
                try fallbackStream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
                addAudioOutput(fallbackStream, captureAudio && audioApp == nil)
                try await fallbackStream.startCapture()
                self.stream = fallbackStream
                self.isRunning = true
                logToFile("[OK] Fallback display stream running at \(width)x\(height) @ \(fps) FPS")
                if #available(macOS 13.0, *), let app = audioApp {
                    await startAppAudio(display: display, app: app)
                }
            }
        } catch {
            if (error as NSError).code == -3801 {
                fputs("PERMISSION|screen\n", stderr)
            }
            logToFile("[FATAL] Error initializing ScreenCaptureKit: \(error)")
            exit(1)
        }
    }

    // startAppAudio captures only what one application plays. The stream still needs a video
    // size; a tiny one at one frame per second costs next to nothing and is never read.
    @available(macOS 13.0, *)
    func startAppAudio(display: SCDisplay, app: SCRunningApplication) async {
        let filter = SCContentFilter(display: display, including: [app], exceptingWindows: [])
        let config = SCStreamConfiguration()
        config.width = 2
        config.height = 2
        config.minimumFrameInterval = CMTime(value: 1, timescale: 1)
        config.queueDepth = 3
        config.capturesAudio = true
        config.sampleRate = 48000
        config.channelCount = 2
        config.excludesCurrentProcessAudio = true
        let audio = SCStream(filter: filter, configuration: config, delegate: self)
        do {
            try audio.addStreamOutput(self, type: .audio, sampleHandlerQueue: audioQueue)
            try await audio.startCapture()
            audioStream = audio
            audioEnabled = true
            logToFile("[AUDIO] Capturing audio of '\(app.applicationName)' (pid \(app.processID)) only")
        } catch {
            logToFile("[AUDIO] App audio unavailable: \(error)")
        }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard sampleBuffer.isValid else { return }
        if #available(macOS 13.0, *), type == .audio {
            handleAudio(sampleBuffer)
            return
        }
        guard type == .screen, stream !== audioStream else { return }
        guard let pixelBuffer = sampleBuffer.imageBuffer else { return }

        CVPixelBufferLockBaseAddress(pixelBuffer, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, .readOnly) }

        guard let baseAddress = CVPixelBufferGetBaseAddress(pixelBuffer) else { return }
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer)
        let width = CVPixelBufferGetWidth(pixelBuffer)
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let rowBytes = width * 4

        self.frameCount += 1
        if self.frameCount == 1 {
            logToFile("[FRAME] First Metal video frame received: \(width)x\(height), bytesPerRow=\(bytesPerRow), expectedRowBytes=\(rowBytes)")
        } else if self.frameCount % 300 == 0 {
            logToFile("[STATS] \(self.frameCount) frames delivered to FFmpeg")
        }

        if bytesPerRow == rowBytes {
            if !writeAll(fd: STDOUT_FILENO, buffer: baseAddress, count: rowBytes * height) {
                logToFile("[PIPE] STDOUT pipe closed by consumer (writeAll failed)")
                exit(0)
            }
        } else {
            for y in 0..<height {
                let rowPtr = baseAddress.advanced(by: y * bytesPerRow)
                if !writeAll(fd: STDOUT_FILENO, buffer: rowPtr, count: rowBytes) {
                    logToFile("[PIPE] STDOUT pipe closed by consumer (row write failed)")
                    exit(0)
                }
            }
        }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        if stream === audioStream {
            // Losing the sound must not end the video.
            logToFile("[AUDIO] App audio stream stopped: \(error)")
            audioEnabled = false
            audioStream = nil
            return
        }
        logToFile("[STOP] Stream stopped with error: \(error)")
        exit(1)
    }
}

var globalRecorder: AnyObject?

if #available(macOS 12.3, *) {
    if CommandLine.arguments.contains("--list") {
        checkScreenPermission(stdout)
        let sem = DispatchSemaphore(value: 0)
        Task {
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
                for (i, d) in content.displays.enumerated() {
                    print("SCREEN|\(d.displayID)|Display \(i+1) (\(d.width)x\(d.height))")
                }
                var seenTitles = Set<String>()
                for w in content.windows {
                    if let title = w.title, !title.isEmpty, w.frame.width > 50, w.frame.height > 50 {
                        let app = w.owningApplication?.applicationName ?? ""
                        let name = app.isEmpty ? title : "\(app) - \(title)"
                        if !seenTitles.contains(name) {
                            seenTitles.insert(name)
                            print("WIN|\(w.windowID)|\(name)")
                        }
                    }
                }
            } catch {
                if (error as NSError).code == -3801 {
                    print("PERMISSION|screen")
                }
                print("SCREEN|desktop|Primary Display")
            }
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 2.0)
        exit(0)
    } else {
        var width = 1920
        var height = 1080
        var fps = 60
        var targetWinID: CGWindowID? = nil

        if CommandLine.arguments.count >= 2, let w = Int(CommandLine.arguments[1]) { width = w }
        if CommandLine.arguments.count >= 3, let h = Int(CommandLine.arguments[2]) { height = h }
        if CommandLine.arguments.count >= 4, let f = Int(CommandLine.arguments[3]) { fps = f }
        var targetDisplayID: CGDirectDisplayID? = nil
        if CommandLine.arguments.count >= 5 {
            let target = CommandLine.arguments[4]
            if target.hasPrefix("display:"), let id = UInt32(target.dropFirst("display:".count)) {
                targetDisplayID = CGDirectDisplayID(id)
            } else if let winNum = UInt32(target) {
                targetWinID = CGWindowID(winNum)
            }
        }

        let captureAudio = CommandLine.arguments.count >= 6 && CommandLine.arguments[5] == "audio"

        checkScreenPermission(stderr)
        let recorder = ScreenRecorder()
        globalRecorder = recorder
        Task {
            await recorder.start(fps: fps, width: width, height: height, targetWindowID: targetWinID, targetDisplayID: targetDisplayID, captureAudio: captureAudio)
        }
        dispatchMain()
    }
} else {
    logToFile("[ERR] ScreenCaptureKit requires macOS 12.3+")
    exit(1)
}
