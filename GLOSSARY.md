# Glossary

**Installation**: one rexd on this machine. Its data and state folders identify it, and they decide everything else: its daemon, its `state.json`, and the label of its LaunchAgent. Two sets of folders are two installations, and neither touches the other's agent. In code, the `installation` package.

**Config code**: the Lua a plugin loads inside Rex, which defines actions and keys.

**Event script**: the Lua a plugin runs under `rex do`, which the daemon keeps alive.
