# Changelog

## 0.3.1

- `Rexd.app`, a menu bar app that controls rexd, in `macos/`.
- `rexd status --json` has `rex_config`, the `init.lua` rexd is connected to.

## 0.3.0

- Install a repository with no manifest, if it holds a Lua module with a `setup` function. Amber plugins use this format.
- A plugin's config code can `require` other Lua files, from its `rex/lua` or `lua` folder.
- `rexd set` changes a plugin's options. Config code reads `rexd.plugin.options`, and event scripts read `rex.args`.
- Version ranges: `owner/repo@^1.2.0` and `@~1.2.0`.
- `rexd.lock` and `rexd sync` get the same plugins on another machine.

## 0.2.2

- The README and `rexd help update` now say what the checks cover: Rex checks config code, and not event scripts.
- If an installed plugin makes Rex's checker fail, rexd names that plugin and how to fix it, instead of blaming your config. `rexd update` of that plugin can now fix it.
- `rexd status` shows `error` in the table for config code Rex rejects, and `--json` has `config_broken`.
- Processes a script started now stop when the script exits. A crash-looping plugin left one more behind on each restart.
- `rexd update` of a git plugin uses a fresh clone. It could fail on every run after a build step changed a tracked file.
- An installation under another `$HOME` now gets a daemon that uses that home.

## 0.2.1

- Install with Homebrew: `brew install tornikegomareli/tap/rexd`. Go is no longer needed.
- Each release has archives for macOS and Linux, on arm64 and amd64.
- The LaunchAgent keeps the path rexd was started through. Under Homebrew, it named a versioned folder that an upgrade removes.

## 0.2.0

- Each set of folders is now its own installation. With custom `XDG_DATA_HOME` or `XDG_STATE_HOME`, or another `$HOME`, rexd uses a LaunchAgent named after those folders, so `setup` and `teardown` no longer replace or remove the normal agent. If you already run rexd with custom folders, run `launchctl bootout gui/$(id -u)/io.github.tornikegomareli.rexd` and delete `~/Library/LaunchAgents/io.github.tornikegomareli.rexd.plist` before `rexd setup`.
- `rexd teardown` works after Rex is removed.
- A failed `rexd setup` puts back the modules and `rexd.lua` it rewrote.
- A failed copy no longer loses a plugin's old config code.
- One `rexd` command checking for the daemon can no longer make a crashed daemon look alive to another, which could signal an unrelated process.
- `rexd update` prints that it checks the plugin's config, as `rexd install` does.

## 0.1.1

- `rexd update` reports a plugin folder that git cannot read. Before, it printed "pinned to ;" and exited 0.
- Pressing Ctrl-C during `rexd teardown` no longer deletes the LaunchAgent's file while launchd may still be stopping the agent.
- The hint for a missing Rex links to Rex's public site.

## 0.1.0

The first version.

- Install plugins from GitHub, any git URL, a local copy, or a link for development.
- Add a plugin's config code to Rex, with Rex's own checker deciding before anything is kept.
- Keep a plugin's event scripts running, restarting them after a delay and waiting while Rex is away.
- Start the daemon at login on macOS with a LaunchAgent.
- `setup`, `teardown`, `install`, `remove`, `update`, `list`, `status`, `logs` and `daemon`.
- An example plugin, `bell-notify`.
