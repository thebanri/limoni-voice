import Foundation
import ScreenCaptureKit
import CoreMedia
import CoreVideo

@available(macOS 12.3, *)
class ScreenRecorder: NSObject, SCStreamOutput, SCStreamDelegate {
    var stream: SCStream?
    let stdoutHandle = FileHandle.standardOutput

    func start(fps: Int = 60, width: Int = 1920, height: Int = 1080) async {
        do {
            let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
            guard let display = content.displays.first else {
                fputs("Error: No display found\n", stderr)
                exit(1)
            }

            let filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
            let config = SCStreamConfiguration()
            config.width = width
            config.height = height
            config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps))
            config.pixelFormat = kCVPixelFormatType_32BGRA
            config.showsCursor = true
            config.queueDepth = 5

            let stream = SCStream(filter: filter, configuration: config, delegate: self)
            try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue(label: "screen.capture.queue", qos: .userInteractive))
            try await stream.startCapture()
            self.stream = stream
            fputs("[SCKIT] ScreenCaptureKit stream running at \(width)x\(height) @ \(fps) FPS\n", stderr)
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
        let height = CVPixelBufferGetHeight(pixelBuffer)
        let totalBytes = bytesPerRow * height

        let data = Data(bytes: baseAddress, count: totalBytes)
        stdoutHandle.write(data)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        fputs("Stream stopped with error: \(error)\n", stderr)
        exit(1)
    }
}

if #available(macOS 12.3, *) {
    var width = 1920
    var height = 1080
    var fps = 60

    if CommandLine.arguments.count >= 2, let w = Int(CommandLine.arguments[1]) { width = w }
    if CommandLine.arguments.count >= 3, let h = Int(CommandLine.arguments[2]) { height = h }
    if CommandLine.arguments.count >= 4, let f = Int(CommandLine.arguments[3]) { fps = f }

    let recorder = ScreenRecorder()
    Task {
        await recorder.start(fps: fps, width: width, height: height)
    }
    dispatchMain()
} else {
    fputs("ScreenCaptureKit requires macOS 12.3+\n", stderr)
    exit(1)
}
