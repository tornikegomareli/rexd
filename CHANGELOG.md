# Changelog

## 0.1.0

The first version.

- Install plugins from GitHub, any git URL, a local copy, or a link for development.
- Add a plugin's config code to Rex, with Rex's own checker deciding before anything is kept.
- Keep a plugin's event scripts running, restarting them after a delay and waiting while Rex is away.
- Start the daemon at login on macOS with a LaunchAgent.
- `setup`, `teardown`, `install`, `remove`, `update`, `list`, `status`, `logs` and `daemon`.
- An example plugin, `bell-notify`.
