Release builds place a prebuilt universal ScreenCaptureKit helper here as `limoni-sck`
(built on macOS from `../mac_capture.swift`, see `.github/workflows/build-and-release.yml`).
When it is absent the helper is compiled with `swiftc` on the user's Mac on first use.
