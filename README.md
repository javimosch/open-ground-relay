# Open Ground relay

The co-op relay for Open Ground: two to four players behind home routers meet through it with a join
code (`XFR1-KILO-47`). Both games connect *out* to the relay; it pairs them and pipes bytes. It runs no game logic, keeps no data on disk
and needs about 3 MB of RAM and almost no CPU. A battle sends a few KB/s.

## Host one

You need a machine with a public IP (a 3 EUR VPS is plenty) and TCP port 47160 open.

```sh
# Linux, amd64 or arm64: download the binary from Releases, then
sudo ./install.sh XFR1           # your relay id: 2-6 letters/digits, a letter first, not taken in the registry
```

or with Docker:

```sh
docker run -d --restart always -p 47160:47160 ghcr.io/javimosch/open-ground-relay -region XFR1
```

Check it from another machine: `printf 'OGR1 PROBE\n' | nc -w 3 YOUR_IP 47160` must print `PROBE XFR1 0/<cap>` (battles open / the most it takes).

Players can use it right away: a host sets `OPENGROUND_RELAY=YOUR_IP:47160`, or anyone joins a code that starts with your id once the
game knows your relay.

## Get listed in the game

Open a pull request on [open-ground-community](https://github.com/javimosch/open-ground-community) adding your relay to `relays/relays.json`. A check probes it; once merged, the official
relays pick it up within the hour and every game lists it as a **community relay**. Players choose whether Auto may pick community
relays (off by default); a code made on your relay always joins.

## How many battles

A battle costs the relay a few KB/s and a handful of connections. With no `-max-sessions`, the relay works its cap out at start from
the open-file limit and half the free RAM (the log says which), and from `-max-mbps` if you give it your uplink: set that on a home line or
a metered VPS (`-max-mbps 20`: about 200 battles, and new hosts are refused above 90 % of it). Games see `used/cap` in every probe and open
on a relay with room; a host turned away as full tries the next best relay.

## Rules for a listed relay

- Do not read, change or log the game traffic beyond what the relay prints (code, version, the joiners' addresses for rate limits).
- Keep it up: three days without answering and it is marked down; a month and it is removed.
- One id per relay; give a contact that answers.
- Do not run a modified relay under a listed id without saying so in the pull request.

## Protocol

One text line opens each connection (`OGR1 HOST|JOIN|ACCEPT|PROBE|LIST|RELAYS ...`); see the comment at the top of `main.go`.
`OGR1` is the wire version; the game hides relays that do not speak it.

## Build

```sh
go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o og-relay .
```

Licence: MIT.
