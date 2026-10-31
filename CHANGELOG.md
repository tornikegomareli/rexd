# Changelog

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
