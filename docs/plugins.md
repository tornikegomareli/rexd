# Writing a plugin

A plugin is a folder with a manifest. It can have config code, event scripts, or both. [`examples/bell-notify`](../examples/bell-notify) is a working template.

```
my-plugin/
  rex-plugin.toml
  rex/config.lua     config code, loaded into Rex's config
  rex/notify.lua     an event script, kept running by the daemon
  lua/my-plugin/     more Lua files, for require (rex/lua if lua is for another program)
```

## Config code or event script

They run in different places, and they can do different things.

| | Config code | Event script |
| --- | --- | --- |
| Runs | inside the Rex server | as its own process, started by the rexd daemon |
| Can define actions and keys | yes | no |
| Can receive events with `rex.on` | no | yes |
| Can use `rex.client.queue` | in actions | yes |
| Has `rex.sleep`, `rex.wait`, `rex.after` | no | yes |
| Environment for `rex.exec` | the Rex server's | the daemon's, plus the variables below |

Use config code for things you press. Use an event script for things that react. A plugin often has both.

## The manifest

`rex-plugin.toml` sits at the top of the plugin.

```toml
id = "acme.bell-notify"
name = "Bell notify"
version = "0.1.0"
description = "Shows a notification when a terminal rings its bell."
homepage = "https://github.com/acme/bell-notify"
license = "MIT"
rex_api = 7

config = "rex/config.lua"

[[scripts]]
name = "notify"
path = "rex/notify.lua"

[[build]]
run = ["go", "build", "-o", "bin/helper", "./cmd/helper"]
```

| Key | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Lowercase letters, digits and hyphens, in at least two parts separated by dots. Use your name first, such as `acme.bell-notify`. At most 64 characters. |
| `name` | yes | The plugin's name for people |
| `version` | yes | The version, with no spaces |
| `description` | no | One sentence |
| `homepage` | no | A URL |
| `license` | no | An SPDX identifier |
| `rex_api` | no | The lowest Rex API revision the plugin works with. `rex version --json` shows the current one as `api_revision`. |
| `config` | no* | The Lua file with the config code |
| `scripts` | no* | The event scripts |
| `scripts.name` | yes | Lowercase letters, digits and hyphens. It names the script in `rexd status` and in its log file. |
| `scripts.path` | yes | The Lua file |
| `build` | no | Commands to run, in order |
| `build.run` | yes | The program and its arguments. No shell reads them. |

\* A plugin needs at least one of `config` and `scripts`.

Paths are relative to the plugin and must stay inside it. rexd ignores keys it does not know and prints a warning, so a plugin written for a newer rexd still loads.

## Config code

rexd loads this file into Rex's config. It defines actions and binds keys, as Rex's [configuration docs](https://www.superlogical.com/rex/docs/customize/config) describe.

```lua
-- Read this at the top of the file. It is only set while the file loads.
local plugin = rexd.plugin

rex.action{
  name = "bell_notify_test",
  title = "Bell Notify: Send a Test Notification",
  run = function()
    rex.exec{ plugin.dir .. "/bin/helper", "test" }
  end,
}

rex.bind("ctrl+alt+shift+b", "bell_notify_test")
```

`rexd.plugin` has four fields: `id`, `version`, `dir`, the plugin's folder, and `options`, the values from `rexd set`. An action runs later, so keep what you need in a local, as above. `rexd.plugins[id]` holds the same table for every plugin that has config code.

Some rules follow from how Rex loads config:

- **More files go in `lua/`.** rexd adds the plugin's `rex/lua` folder, or else its `lua` folder, to `package.path`. All plugins share Lua's module names, so start yours with your plugin's name: `require("my-plugin.helpers")`.
- **Action names are shared.** All plugins and the user's config use one namespace, so start yours with your plugin's name. Action names allow lowercase letters, digits and underscores.
- **Rex API calls only work inside an action or a function bound to a key.** They fail while the file loads, because no client exists then. `rex.action` and `rex.bind` are the exceptions.
- **The user's keys win.** rexd loads before the rest of `init.lua`, and the newest binding for a key wins. Choose keys that are unlikely to clash.

rexd loads your file in Rex's checker on install and update, and refuses a file with errors. If your file breaks later, which can happen with `--link`, Rex loads everything else and `rexd status` shows the error.

## Event scripts

An event script is a normal script for `rex do`. See Rex's [Lua events docs](https://www.superlogical.com/rex/docs/automate/lua-events) for the events you can hear.

```lua
rex.on("terminal.bell", function(ctx, ev)
  local view = rex.session.view{}
  print("bell in " .. view.label)
end)

print("listening")
```

The daemon runs it as `rex do notify.lua --all`, so it hears every session, including sessions created later. Its options are in `rex.args`, and it restarts when they change. Its working directory is the plugin's folder, and it has these variables:

| Variable | Value |
| --- | --- |
| `REXD_PLUGIN_ID` | The plugin's id |
| `REXD_PLUGIN_VERSION` | The plugin's version |
| `REXD_PLUGIN_DIR` | The plugin's folder |
| `REXD_SCRIPT` | The script's name |

What to expect:

- **A script must keep running.** It needs at least one `rex.on` handler, or it exits at once. The daemon treats any exit as a failure and restarts it.
- **Handler errors do not stop it.** Rex reports the error in the log, and the script keeps going. An error at the top level of the file does stop it, and it then restarts after a delay.
- **Restarts back off.** The delay starts at 1 second, doubles up to 1 minute, and starts over once a script has run for a minute. After 5 exits within 2 minutes, `rexd status` shows it as crashing, and it retries once a minute.
- **Rex going away is not a failure.** The script exits with Rex, waits, and starts again when Rex is back. This does not count as a restart.
- **It needs a session to start.** While Rex has no sessions, the script waits. It cannot hear the event for the session that ends the wait.
- **Do not rely on `rex.session_id`.** It is whichever session the daemon picked for starting the script, which may close later. Use the fields of the event.
- **`print` goes to the log.** Each line gets a timestamp. See it with `rexd logs PLUGIN`.

The daemon's `PATH` comes from the shell that ran `rexd setup`, because launchd starts programs with almost no environment. Running `rexd setup` again updates it.

## Build steps

Build steps run in order on install and update, with the first three variables above set. If one fails, nothing changes. Each command runs directly, so to use shell syntax, run `sh -c`:

```toml
[[build]]
run = ["sh", "-c", "npm ci && npm run build"]
```

`rexd install` prints each command before it runs it.

A step runs in a temporary copy of the plugin, which moves into place afterwards. So write files with relative paths. `REXD_PLUGIN_DIR` is where the plugin ends up, for a build that has to embed that path. That folder may not exist yet, or may still hold the old version, so do not write to it.

## Options

A person sets options with `rexd set my.plugin color=red`. Values are strings, numbers or booleans. An option that is not set is `nil`, so give each one a default in your code, and list them in your README:

```lua
local color = rexd.plugin.options.color or "blue"
```

## A plugin with no manifest

rexd also installs a repository with no `rex-plugin.toml`, if `rex/lua` or `lua` holds a module that returns a table with a `setup` function. rexd calls `setup` with the plugin's options. This is the format Amber plugins use.

The id comes from the source: `owner.repo` for a repository, without a trailing `.rex`, and `local.folder` for a local folder. The version is the short commit. With several modules, rexd starts the one named after the repository. Such a plugin cannot have event scripts or build steps, because a manifest names those.

## Developing a plugin

Install it with a link, so rexd uses your folder directly:

```sh
rexd install ./my-plugin --link
```

Then, as you work:

| You changed | Run |
| --- | --- |
| Config code | `rex config reload`, or `rexd update PLUGIN` to also run the check |
| An event script | `rexd update PLUGIN`, which restarts it |
| The manifest | `rexd update PLUGIN` |

These help when something does not work:

```sh
rexd status              # what runs, what failed, and config code errors
rexd logs PLUGIN -f      # a script's output as it happens
rexd logs --daemon       # what the daemon did
rex keymap               # the keys and actions Rex loaded
```

Removing a linked plugin deletes the link and leaves your folder alone.

## Publishing

Push the plugin to a public git repository, with `rex-plugin.toml` at the top. People install it with `rexd install owner/repo`. Tag releases as `v1.2.3`, so they can pin one with `rexd install owner/repo@v1.0.0`, or follow a range with `owner/repo@^1.0.0`. A pinned plugin stays where it is on `rexd update`, and a range stays in its range.
