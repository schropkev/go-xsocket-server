# `go-xsocket-server`: xsocket-server in pure Go.

`go-xsocket-server` is an implementation of original [xsocket-server](https://github.com/koro666/xsocket) in Go.

## Build

Clone the repository and compile with `Go` as usual.

```
git clone https://github.com/schropkev/go-xsocket-server
cd go-xsocket-server
go build
```

If you wish to compile this program into a static binary:

`CGO_ENABLED=0 go build`

## Usage

Its usage is:

`./go-xsocket-server [--chmod=PERMISSION] [--systemd] </path/to/xsocket-server/socket|@xsocket-abstract-socket>`

`--chmod` makes sense only if the `go-xsocket-server` Unix socket is a file path, otherwise ignored. `--systemd` is optional for making `go-xsocket-server` interacts with `systemd`.

**Examples:**

`./go-xsocket-server --chmod=0700 /var/tmp/xsocket/netns1`

`./go-xsocket-server @vrf2-xsocket`

**ALWAYS run `go-xsocket-server` as an unpriviled user for security reasons:**

`sudo -u someuser -- go-xsocket-server --chmod 0700 --systemd /path/to/xsocket-server/socket`

`sudo ip netns exec somenetns sudo -u someuser -- go-xsocket-server --chmod 0700 /path/to/xsocket-server/socket`

`sudo ip vrf exec sudo -u someuser -- vrf-blue go-xsocket-server @xsocket-server_socket`

It is possible also to mark `go-xsocket-server` sockets with firewall marks by using [this script](https://github.com/zhangyoufu/fwmark). Just run `go-xsocket-server` as an unprivileged user using this script by using `sudo`; all the listening points and outgoing connections will follow the firewall mark specified in the script without needing elevated privileges such as `CAP_NET_ADMIN` (needed for `SO_MARK`).

`sudo ./fwmark.py 123 sudo -u someuser ./go-xsocket-server @xs-socket`

--------------------

## Thanks

- [@koro666](https://github.com/koro666) for his awesome project [xsocket](https://github.com/koro666/xsocket).

--------------------

Created on June 15, 2026.
