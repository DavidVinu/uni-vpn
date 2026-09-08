# uni-vpn

Heidelberg University's VPN (Cisco AnyConnect) only for selected websites in the browser.
The tunnel comes up on the first request to a listed domain, password and TOTP secret live
in the operating system's keyring, and after 15 minutes without traffic the tunnel is torn
down again. Nothing else on the machine is touched: no root, no tun device, no changes to
routes or DNS.

## How it works

A small Python daemon (standard library only) runs as a user service. It drives
`openconnect` in `--script-tun` mode with `ocproxy`, so the VPN exists only as a SOCKS5
proxy on `127.0.0.1:1080`. A PAC rule in the system proxy settings sends the listed
domains there and everything else directly. A status page on `http://127.0.0.1:1081/`
shows the state and lets you connect or disconnect by hand.

## Status

Targets Ubuntu 24.04 with Chrome and Firefox. macOS 14+ on Apple Silicon is prepared but
not yet tested on real hardware. The design lives in
`docs/superpowers/specs/2026-09-07-uni-vpn-design.md`; the implementation is on the `impl`
branch until it is merged here.

## License

MIT
