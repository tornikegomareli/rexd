<h1 align="center">rexd</h1>

<h3 align="center">A plugin manager and process supervisor for Rex.</h3>

[Rex](https://rex.superlogical.com) runs plugin code in two places. Config code defines actions and keys, and it has to be in Rex's `init.lua`. Event scripts react to things like a terminal bell, and they only receive events while a `rex do` process stays alive. Rex ships no installer for the first part, and nothing that keeps the second part running.

rexd does both. It installs a plugin from GitHub, adds its config code to Rex, and keeps its event scripts running.

```
$ rexd install tornikegomareli/some-plugin
Cloning https://github.com/tornikegomareli/some-plugin.git
Checking the plugin's config
Installed tornikegomareli.some-plugin 0.1.0

$ rexd status
Daemon: running, pid 79409
Rex:    running

PLUGIN            VERSION  CONFIG  SCRIPT  STATE                                   RESTARTS
acme.crashy       0.0.1    -       boom    exited with status 1, restarting in 4s  3
rexd.bell-notify  0.1.0    loaded  notify  running, pid 79542                      0
```

## Install

```sh
git clone https://github.com/tornikegomareli/rexd.git
cd rexd
make install
```

This puts `rexd` in your Go bin folder, usually `~/go/bin`. If that folder is not on your `PATH`, choose another one:

```sh
GOBIN="$HOME/.local/bin" make install
```

To update rexd itself, run `git pull`, then `make install` and `rexd setup`. `rexd setup` restarts the daemon with the new version.

Requirements:

- Rex. rexd finds the `rex` command on your `PATH`, or the copy inside the macOS app. Set `REXD_REX` to use another one.
- Go 1.24 or newer to build it, and git to install plugins from GitHub.
- macOS, Linux or FreeBSD. On macOS, `rexd setup` makes the daemon start at login. On the others, see [Running without a LaunchAgent](#running-without-a-launchagent).

## Quick start

```sh
rexd setup                               # connect to Rex and start the daemon
rexd install ./examples/bell-notify      # install the example plugin
```

Ring a bell in any Rex terminal with `printf '\a'`, and a notification names the session.

`rexd setup` is safe to run again, and it repairs whatever is missing.

## Commands

| Command | What it does |
| --- | --- |
| `rexd setup` | Connects rexd to Rex and starts the daemon |
| `rexd install SOURCE` | Installs a plugin |
| `rexd remove PLUGIN` | Removes a plugin |
| `rexd update [PLUGIN...]` | Updates plugins, all of them if none are named |
| `rexd list` | Lists installed plugins |
| `rexd status` | Shows the daemon, plugins and event scripts |
| `rexd logs PLUGIN` | Prints the output of a plugin's event scripts |
| `rexd teardown` | Disconnects rexd from Rex and stops the daemon |
| `rexd daemon` | Runs the daemon in the foreground |

`rexd help COMMAND` shows every flag. `list` and `status` take `--json`.

`SOURCE` is one of these:

```sh
rexd install owner/repo                  # a GitHub repository
rexd install owner/repo@v1.2.0           # at a branch, tag or commit
rexd install https://gitlab.com/a/b.git  # any git URL
rexd install ./my-plugin                 # a copy of a local folder
rexd install ./my-plugin --link          # a link to it, for development
```

## Writing a plugin

A plugin is a folder with a manifest, `rex-plugin.toml`:

```toml
id = "acme.bell-notify"
name = "Bell notify"
version = "0.1.0"
rex_api = 7

config = "rex/config.lua"   # actions and keys, loaded into Rex's config

[[scripts]]                 # event scripts the daemon keeps running
name = "notify"
path = "rex/notify.lua"
```

An event script is a normal Rex Lua script that registers handlers:

```lua
rex.on("terminal.bell", function(ctx, ev)
  rex.exec{ "osascript", "-e", 'display notification "Bell!" with title "Rex"' }
end)
```

Develop it with `rexd install ./my-plugin --link`, and run `rexd update acme.bell-notify` to reload it. [`examples/bell-notify`](examples/bell-notify) is a working template, and the [plugin guide](docs/plugins.md) has the full reference.

## How it stays safe

rexd edits your Rex config, so a mistake there must not cost you your setup.

- **Rex checks every change.** rexd loads a plugin in Rex's own checker before keeping it. If the check fails, every step undoes itself and your config stays as it was.
- **An update never replaces a working version with a broken one.** The new version is built beside the old one, and it only moves into place after it passes the check.
- **One broken plugin stays contained.** If a plugin breaks later, its error stays with it, and your own keys and other plugins keep working. `rexd status` says what is wrong.
- **Your keys win.** `require("rexd")` goes first in `init.lua`. In Rex the newest binding for a key wins, so anything you bind below it overrides a plugin.
- **It never starts Rex.** Every `rex` command runs with `--autostart=false`. If you quit Rex, scripts wait until you open it again.

A plugin's build steps and scripts run as you, like any program you install. Install plugins you trust.

## What it does to your machine

| Path | What it is |
| --- | --- |
| `~/.config/rex/init.lua` | Your config, with one added line at the top |
| `~/.config/rex/rexd.lua` | Generated, loads every plugin |
| `~/.config/rex/rexd/plugins/` | The plugins' config code |
| `~/.local/share/rexd/plugins/` | Installed plugins |
| `~/.local/state/rexd/` | State, status and logs |
| `~/Library/LaunchAgents/io.github.tornikegomareli.rexd.plist` | Starts the daemon at login |

The two `.local` folders follow `XDG_DATA_HOME` and `XDG_STATE_HOME`.

## Remove

```sh
rexd teardown          # disconnect; plugins stay, and rexd setup brings them back
rexd teardown --purge  # also delete plugins, state and logs
```

Both remove the line from `init.lua`, the generated files and the LaunchAgent.

## Running without a LaunchAgent

Use `rexd setup --no-service`, then keep `rexd daemon` running however you like. For a systemd user service, `~/.config/systemd/user/rexd.service`:

```ini
[Unit]
Description=rexd

[Service]
ExecStart=%h/go/bin/rexd daemon
Restart=always

[Install]
WantedBy=default.target
```

rexd has only been tested on macOS so far. It builds on Linux and FreeBSD, and CI runs its unit tests on Linux.

## Limits

- An event script can only start once Rex has at least one session. It misses events from before it started.
- A plugin's config code is one file, because Rex's `require` only finds files beside `init.lua`.
- Windows is not supported. rexd uses Unix signals and file locks.

## How it works

The [architecture notes](docs/architecture.md) explain the design, and the Rex behavior it is built on. Each fact there was measured against a real Rex server.

## Development

```sh
make test      # unit tests, with the race detector
make e2e       # end-to-end tests against a real Rex
make e2e-race  # the same, with a race-enabled rexd
make lint      # gofmt and go vet
```

The end-to-end tests need Rex installed. Each test starts its own server with a temporary socket, state and config, and it checks the server's config path before doing anything else, so they can run while you work in Rex. They use launchd for real, under labels of their own.

## License

[MIT License](LICENSE).
