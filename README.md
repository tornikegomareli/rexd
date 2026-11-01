<h1 align="center">rexd</h1>

<h3 align="center">Install plugins for Rex, and keep them running.</h3>

<p align="center">
  <img src=".github/assets/film-install.png" width="100%" alt="A Rex window: rexd install adds the bell-notify plugin, and rexd status shows its event script running" />
</p>

[Rex](https://www.superlogical.com/rex) has no plugin installer. A plugin adds actions and keys, or reacts to events, such as showing a notification when a terminal rings its bell. rexd installs one with a single command, and a daemon keeps its scripts running.

## Install

```sh
brew install tornikegomareli/tap/rexd
rexd setup
```

`rexd setup` connects rexd to Rex and starts the daemon. It is safe to run again. Run it after `brew upgrade rexd` too, to restart the daemon with the new version.

Without Homebrew, use an archive from the [releases](https://github.com/tornikegomareli/rexd/releases), or run `go install github.com/tornikegomareli/rexd/cmd/rexd@latest`.

You need Rex, and git to install plugins from a repository. rexd is tested on macOS. It builds on Linux and FreeBSD: there, run `rexd setup --no-service`, and keep `rexd daemon` running yourself.

## Try it

```sh
git clone https://github.com/tornikegomareli/rexd.git
rexd install ./rexd/examples/bell-notify
```

Ring a bell in a Rex terminal with `printf '\a'`, and a macOS notification names the session.

## Install any plugin

```sh
rexd install owner/repo                  # a GitHub repository
rexd install owner/repo@v1.2.0           # at a branch, tag or commit
rexd install owner/repo@^1.2.0           # the highest 1.x tag, and updates stay there
rexd install https://gitlab.com/a/b.git  # any git URL
rexd install ./my-plugin                 # a copy of a local folder
rexd install ./my-plugin --link          # a link to it, for development
```

Rex loads a new plugin's actions and keys straight away. Your own keys win over a plugin's.

A repository needs no `rex-plugin.toml` if it holds a Lua module with a `setup` function, the format [Amber](https://github.com/rawnly/amber) plugins use.

A plugin's build steps and scripts run as you, like any program you install. Install plugins you trust.

## Scripts stay alive

If a plugin's script dies, the daemon starts it again and waits longer after each crash. If you quit Rex, scripts wait until you open it again. `rexd status` shows what every script is doing, and `rexd logs` shows why one failed.

<p align="center">
  <img src=".github/assets/film-restart.png" width="100%" alt="A Rex window: rexd status shows a script that exited and restarts in 17 seconds, beside one that runs. rexd logs shows the error: the API key is not set" />
</p>

## A broken plugin does not break your config

Rex's own checker reads a plugin's config code before rexd keeps it. If the check fails, rexd undoes every step, and an update keeps the old version.

Rex does not check event scripts, so a broken script installs like a working one. It then shows as failing in `rexd status`.

<p align="center">
  <img src=".github/assets/film-check.png" width="100%" alt="A Rex window: rexd install refuses a plugin with a syntax error in its config code, and rexd list shows the other plugins are still installed" />
</p>

## Options

```sh
rexd set acme.foo color=red size=3   # set two options
rexd set acme.foo color=             # remove one
rexd set acme.foo                    # show them
```

Rex's checker reads the plugin with the new values, and a failure changes nothing. The plugin's README says which options it has.

## The same plugins on another machine

rexd writes `rexd.lock` beside Rex's `init.lua` on every change, with the commit and options of each plugin from git. Keep it in your dotfiles. On the other machine:

```sh
rexd setup
rexd sync
```

`rexd sync` installs what is missing and moves the rest to their locked commits. It removes nothing.

## Commands

| Command | What it does |
| --- | --- |
| `rexd setup` | Connects rexd to Rex and starts the daemon |
| `rexd install SOURCE` | Installs a plugin |
| `rexd remove ID` | Removes a plugin. `rexd list` shows each ID |
| `rexd update [ID...]` | Updates plugins, all of them if none are named |
| `rexd set ID [KEY=VALUE...]` | Shows or changes a plugin's options |
| `rexd sync` | Makes the plugins match `rexd.lock` |
| `rexd list` | Lists installed plugins |
| `rexd status` | Shows the daemon, plugins and event scripts |
| `rexd logs ID` | Prints the output of a plugin's event scripts |
| `rexd teardown` | Disconnects rexd from Rex and stops the daemon |

`rexd help COMMAND` shows every flag.

## Remove

```sh
rexd teardown --purge
brew uninstall rexd
```

`--purge` also deletes plugins, state and logs. Without it, plugins stay, and `rexd setup` brings them back.

## What it changes on your machine

| Path | What it is |
| --- | --- |
| `~/.config/rex/init.lua` | Your config, with one added line at the top |
| `~/.config/rex/rexd.lua` and `rexd/` | Generated: the plugins' config code |
| `~/.config/rex/rexd.lock` | Generated: the plugins from git, for another machine |
| `~/.local/share/rexd/` | Installed plugins |
| `~/.local/state/rexd/` | State, status and logs |
| `~/Library/LaunchAgents/io.github.tornikegomareli.rexd.plist` | Starts the daemon at login |

## Limits

- Rex is in beta, and an update to it can break rexd.
- An event script starts once Rex has a session. It misses events from before that.
- Windows is not supported.

## More

- [Writing a plugin](docs/plugins.md), with [`examples/bell-notify`](examples/bell-notify) as a template.
- [How rexd works](docs/architecture.md).
- `make test` runs the unit tests, and `make e2e` runs the end-to-end tests against a real Rex, on servers of their own.

## License

[MIT License](LICENSE).
