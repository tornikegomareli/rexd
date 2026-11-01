# Glossary

**Installation**: one rexd on this machine. Its data and state folders identify it, and they decide everything else: its daemon, its `state.json`, and the label of its LaunchAgent. Two sets of folders are two installations, and neither touches the other's agent. In code, the `installation` package.

**Config code**: the Lua a plugin loads inside Rex, which defines actions and keys.

**Event script**: the Lua a plugin runs under `rex do`, which the daemon keeps alive.

**Options**: the values a person sets for one plugin with `rexd set`. Config code reads them from `rexd.plugin.options`, and an event script from `rex.args`.

**Starter**: the config code rexd writes for a plugin with no manifest. It calls the `setup` function of the plugin's Lua module.

**Lock**: `rexd.lock`, beside `init.lua`. It holds the commit and options of each plugin from git, so `rexd sync` can get the same plugins on another machine.
