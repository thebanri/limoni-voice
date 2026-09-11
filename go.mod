module limoni-voice

go 1.27.1

require (
	github.com/godbus/dbus/v5 v5.2.2
	github.com/gorilla/websocket v1.5.3
	github.com/thebanri/limoni v0.2.4
	github.com/thebanri/limoni-voice v1.4.52
)

require golang.org/x/sys v0.47.0 // indirect

replace github.com/thebanri/limoni-voice => ./
