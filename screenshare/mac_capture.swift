import Foundation
import Darwin
import ScreenCaptureKit
import CoreMedia
import CoreVideo

_ = Darwin.signal(SIGPIPE, SIG_IGN)

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

@available(macOS 12.3, *)
class ScreenRecorder: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    var isRunning = false

    func start(fps: Int = 60, width: Int = 1920, height: Int = 1080, targetWindowID: CGWindowID? = nil) async {
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
            var filter: SCContentFilter?

            if let winID = targetWindowID, let targetWin = content.windows.first(where: { $0.windowID == winID }) {
                filter = SCContentFilter(desktopIndependentWindow: targetWin)
            } else if let display = content.displays.first {
                filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
            }

            guard let activeFilter = filter else {
                fputs("Error: No display or window found\n", stderr)
                exit(1)
            }

            let config = SCStreamConfiguration()
            config.width = width
            config.height = height
            config.scalesToFit = true
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 8

            let stream = SCStream(filter: activeFilter, configuration: config, delegate: self)
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
            try await stream.startCapture()
            self.stream = stream
            self.isRunning = true
            fputs("[SCKIT] ScreenCaptureKit stream active at \(width)x\(height) @ \(fps) FPS\n", stderr)
        } catch {
            fputs("Error starting ScreenCaptureKit: \(error)\n", stderr)
            exit(1)
        }
    }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard sampleBuffer.isValid, type == .screen else { return }
        guard let pixelBuffer = sampleBuffer.imageBuffer else { return }

        CVPixelBufferLockBaseAddress(pixelBuffer, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(pixelBuffer, .readOnly) }

        guard let baseAddress = CVPixelBufferGetBaseAddress(pixelBuffer) else { return }
        let bytesPerRow = CVPixelBufferGetBytesPerRow(pixelBuffer)
        let width = CVPixelBufferGetWidth(pixelBuffer)
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let rowBytes = width * 4

        if bytesPerRow == rowBytes {
            if !writeAll(fd: STDOUT_FILENO, buffer: baseAddress, count: rowBytes * height) {
                exit(0)
            }
        } else {
            for y in 0..<height {
                let rowPtr = baseAddress.advanced(by: y * bytesPerRow)
                if !writeAll(fd: STDOUT_FILENO, buffer: rowPtr, count: rowBytes) {
                    exit(0)
                }
            }
        }
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        fputs("[SCKIT] Stream stopped with error: \(error)\n", stderr)
        exit(1)
    }
}

// Retain globally to prevent Swift ARC from deallocating the recorder
var globalRecorder: AnyObject?

if #available(macOS 12.3, *) {
    if CommandLine.arguments.contains("--list") {
        let sem = DispatchSemaphore(value: 0)
        Task {
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
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
        if CommandLine.arguments.count >= 5, let winStr = CommandLine.arguments[4] as String?, !winStr.isEmpty && winStr != "desktop" && winStr != "portal" {
            if let winNum = UInt32(winStr) {
                targetWinID = CGWindowID(winNum)
            }
        }

        let recorder = ScreenRecorder()
        globalRecorder = recorder
        Task {
            await recorder.start(fps: fps, width: width, height: height, targetWindowID: targetWinID)
        }
        dispatchMain()
    }
} else {
    fputs("ScreenCaptureKit requires macOS 12.3+\n", stderr)
    exit(1)
}
