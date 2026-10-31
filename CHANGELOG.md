# Changelog

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
