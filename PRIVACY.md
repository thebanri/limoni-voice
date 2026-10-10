# Privacy Policy

Last updated: 2026-10-10

Limoni Voice is a free, open-source voice chat and screen sharing program. It has no accounts,
no analytics, no telemetry and no crash reporting. This page lists everything that leaves your
computer when you use it, and who receives it.

## What other people in a room receive

Voice, video, screen shares, chat messages and files go to the other members of the room you
join, directly when the network allows it. They are encrypted end to end with a group key
(AES-256-GCM) that only the room's members hold. Members connected directly can see your IP
address, as with any peer-to-peer program.

## The public relay

Unless you set your own relay or use LAN-only mode, Limoni Voice connects to the public relay at
`voice.thebanri.dpdns.org`, run by the maintainer. It is used to create and join rooms and to
forward traffic when a direct connection is not possible.

- The relay sees your IP address, the room code, when you connect and disconnect, and the
  encrypted packets it forwards. It cannot read their content.
- Its log records the IP address and room code when a room is created and when a member joins
  or is refused. The log is used only to run and protect the relay, and is never sold or
  shared.
- Connections to the relay pass through Cloudflare, which processes them under
  [Cloudflare's privacy policy](https://www.cloudflare.com/privacypolicy/).

You can run your own relay (the `relay-server` in this repository) or set the relay to `off`
for LAN-only use, and then nothing is sent to the public relay.

## STUN servers

To find a direct path between members, Limoni Voice asks public STUN servers
(`stun.l.google.com`, `stun1.l.google.com`, `stun.cloudflare.com`, `stun.nextcloud.com`) for
your public IP address and port. These servers see your IP address and nothing else.

## Updates

Shortly after start, Limoni Voice asks GitHub (`api.github.com`) for the latest release and,
when there is a newer one, downloads it from `github.com`. GitHub sees your IP address and a
User-Agent with your Limoni Voice version, under
[GitHub's privacy statement](https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement).
A copy installed by a package manager (Homebrew, AUR, winget) is updated by that package
manager instead.

## Installer and invite links

The Windows installer may install FFmpeg and mpv for screen sharing through winget, which
downloads them from their publishers.

Invite links open `limoni-voice-website.vercel.app/join#<room key>`. The room key is in the
part after `#`, which browsers never send to the server. The website is hosted on Vercel, which
sees your IP address as for any website; it has no analytics or trackers.

## On your computer

Your settings (`config.json`) and the log file stay on your computer. Limoni Voice never
uploads them.

## Contact and changes

Questions go to [GitHub issues](https://github.com/thebanri/limoni-voice/issues). Changes to
this policy are in this file's history.
