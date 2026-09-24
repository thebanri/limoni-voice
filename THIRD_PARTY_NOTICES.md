# Third-party notices

Limoni Voice is licensed under the GNU Affero General Public License v3.0 (see
[LICENSE](LICENSE)). It is built with, or can work together with, the software below,
each under its own license.

## Programs used for screen sharing

Limoni Voice does not include these programs; it starts them when they are installed.

| Program | License | Source |
|---|---|---|
| [FFmpeg](https://ffmpeg.org) | LGPL 2.1 or later / GPL 2 or later | https://ffmpeg.org/download.html |
| [mpv](https://mpv.io) | GPL 2 or later / LGPL 2.1 or later | https://github.com/mpv-player/mpv |
| [GPU Screen Recorder](https://git.dec05eba.com/gpu-screen-recorder) | GPL 3 | https://git.dec05eba.com/gpu-screen-recorder |

FFmpeg is a trademark of Fabrice Bellard, originator of the FFmpeg project.

## Code ported into Limoni Voice

These are Go ports of C code, kept under the original BSD 3-Clause license; the
copyright notices are at the top of each file.

| Code | Original | Copyright |
|---|---|---|
| `internal/dsp/rnnoise` (noise suppression, with its trained model) | [RNNoise](https://github.com/xiph/rnnoise) v0.1 | 2017 Mozilla, 2007-2017 Jean-Marc Valin, 2005-2017 Xiph.Org Foundation, 2003-2004 Mark Borgerding |
| `internal/dsp/aec.go` (echo cancellation) | [SpeexDSP](https://github.com/xiph/speexdsp) MDF | 2003-2008 Jean-Marc Valin, Xiph.Org Foundation |

## Go libraries compiled into Limoni Voice

Their license texts are in `vendor/` next to each library's source.

| Library | License |
|---|---|
| [Limoni](https://github.com/thebanri/limoni) | Apache 2.0 |
| [purego](https://github.com/ebitengine/purego) | Apache 2.0 |
| [gopus](https://github.com/thesyncim/gopus) | BSD 3-Clause |
| [cpace](https://filippo.io/cpace) | BSD 3-Clause |
| [ristretto255](https://github.com/gtank/ristretto255) | BSD 3-Clause |
| [godbus/dbus](https://github.com/godbus/dbus) | BSD 2-Clause |
| [gorilla/websocket](https://github.com/gorilla/websocket) | BSD 2-Clause |
| [xgb](https://github.com/jezek/xgb) | BSD 3-Clause |
| [pulse](https://github.com/jfreymuth/pulse) | MIT |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto), [x/sys](https://pkg.go.dev/golang.org/x/sys) | BSD 3-Clause |
