// Limoni Voice.app's executable. Limoni Voice is a terminal program, so the bundle opens it in
// Terminal: on a plain launch, and with the room filled in when a limoni:// invite link is
// opened. macOS delivers such links as an Apple Event, which a shell script cannot receive.
//
// The link is passed as a bare argument, never as --join: any web page can open a link, so
// Limoni Voice only fills the room in and waits for the user to press Enter.
import AppKit

func shellQuote(_ s: String) -> String {
    return "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
}

func appleScriptString(_ s: String) -> String {
    let escaped = s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
    return "\"" + escaped + "\""
}

final class Launcher: NSObject, NSApplicationDelegate {
    var handledLink = false

    func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls where url.scheme?.lowercased() == "limoni" {
            let link = url.absoluteString
            if link.count <= 512 {
                launch(argument: link)
                handledLink = true
            }
        }
        NSApp.terminate(nil)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // A link that started the app arrives just after launch; wait for it briefly.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) {
            if !self.handledLink {
                self.launch(argument: nil)
            }
            NSApp.terminate(nil)
        }
    }

    func launch(argument: String?) {
        let binary = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/limoni-voice").path
        var command = "clear; " + shellQuote(binary)
        if let arg = argument {
            command += " " + shellQuote(arg)
        }
        command += "; exit"
        let source = "tell application \"Terminal\"\nactivate\ndo script " + appleScriptString(command) + "\nend tell"
        var error: NSDictionary?
        NSAppleScript(source: source)?.executeAndReturnError(&error)
        if let error = error {
            NSLog("Limoni Voice launcher could not open Terminal: %@", error)
        }
    }
}

let app = NSApplication.shared
let launcher = Launcher()
app.delegate = launcher
app.setActivationPolicy(.accessory)
app.run()
