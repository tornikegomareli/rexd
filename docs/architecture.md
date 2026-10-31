# Architecture

rexd is one Go binary with two jobs. As a command, it changes what is installed. As a daemon, it keeps event scripts running. The two sides share files and a signal, and they never talk over a socket.

```
              writes state.json, sends SIGHUP
 rexd CLI  ───────────────────────────────────▶  rexd daemon  ◀── launchd keeps it alive
    │                                                 │
    │ rex config check                                │ rex do script.lua --all
    │ rex config reload                               │ (one process per script)
    ▼                                                 ▼
 ~/.config/rex/init.lua  ────── read by ─────▶  Rex server  ──── events ───▶ scripts
```

All contact with Rex goes through the `rex` command. rexd never reads Rex's database or speaks its protocol, because both can change while Rex is in beta.

## Packages

| Package | What it does |
| --- | --- |
| `cli` | The command line |
| `plugins` | Install, update, remove, setup and teardown. Install, update and setup are transactions |
| `rexconfig` | The generated `rexd.lua`, the strict check, and edits to `init.lua` |
| `manifest` | Reads and validates `rex-plugin.toml` |
| `daemon` | The single daemon process: lock, PID file, signals |
| `supervisor` | Starts, watches and restarts event scripts |
| `service` | The LaunchAgent |
| `rex` | Runs the `rex` command |
| `state` | `state.json` and `status.json` |
| `logfile`, `lockfile`, `fsutil`, `paths` | Small building blocks |

## How config code reaches Rex

Rex's `require` only loads files beside `init.lua`, so rexd keeps them there:

```
init.lua                        require("rexd") on its first line
rexd.lua                        generated, loads each plugin
rexd/plugins/acme/foo.lua       the config code of plugin acme.foo
```

`rexd.lua` loads each plugin through `pcall`. If a file fails to load in Rex, Rex keeps the result of the last load that worked. Without `pcall`, one broken plugin would freeze the whole config, including the user's own changes.

`pcall` also hides errors, so install and update use a second path. `rexconfig.Check` writes a temporary file beside `init.lua` that requires the plugin directly, and it runs `rex config check` on that file. Rex then reports the plugin's errors. `rexd status` uses the same check, because errors at load time do not appear anywhere the user can see.

`require("rexd")` goes first in `init.lua`. The newest binding for a key wins, so the user's bindings override a plugin's.

## Transactions

Install, update and setup each keep a stack of undo steps. If a step fails, the steps before it undo in reverse order. Rex's checker runs before anything is saved, and `state.json` is written last, so a crash leaves at worst some files that the next run replaces.

An update builds the new version in a staging folder on the same file system, then swaps folders with two renames. A linked plugin has no staging copy, so it updates in place.

Only one command changes things at a time, because they hold a file lock.

## The supervisor

One goroutine owns all state, and everything else posts functions for it to run: process exits, timers, and answers from Rex. So nothing needs a lock.

A script is in one of these states:

```
             Rex and a session are there
  waiting ─────────────────────────────────▶ running
     ▲ ▲                                       │
     │ │       exit, and Rex or its last       │
     │ │       session is gone                 │
     │ └───────────────────────────────────────┤
     │                                         │ exit, with Rex still there
     │  delay is over                          ▼
     └─────────────────────────────  backoff, or crashing
```

Waiting does not count as failing. To tell the two apart, the supervisor asks Rex with `rex ls`:

- While scripts wait, it asks after 2 seconds, then less and less often, up to every 15 seconds. Rex can stay closed for hours, and each question starts a process. So a script starts at most 15 seconds after Rex is back.
- When a script exits, it asks once. If Rex is there, the script failed. If not, the script only went down with Rex.

The exit status plays no part in this. A script can exit with the status rex uses for "server gone" while Rex is fine, and it would then restart as fast as processes can start. Asking Rex also keeps working if Rex changes its exit statuses.

The delay starts at 1 second and doubles up to 1 minute. It starts over once a script has run for a minute. Five failures within 2 minutes mark a script as crashing, and it then retries at the longest delay.

### Scripts left behind

Each script has its own process group, so a Ctrl-C on the daemon's terminal does not hit it, and stopping it reaches whatever it started. This also means a script survives a daemon that dies. launchd would then start a new daemon, and every event would be handled twice.

So `status.json` records each script's process ID and path. A new daemon reads the old file and stops each process whose command line still names its script. The check on the command line matters, because the ID may belong to something else by now.

## Why files and a signal, and no socket

The command line writes `state.json` and sends `SIGHUP`. The daemon reads the file, starts what is new, and stops what is gone. It writes `status.json`, which `rexd status` reads.

So everything rexd knows survives a restart of either side, and the daemon can start with no conversation. `SIGHUP` is safe to repeat, because it only means to look at the file again.

A file lock held for the daemon's lifetime proves it is alive. The kernel releases the lock when the process dies, however it dies. Commands that only ask take the same lock shared, so they do not look like a daemon to each other. So a stale PID file can never be mistaken for a live daemon.

## Why one daemon and not one launchd job per script

launchd could restart each script by itself, with no daemon. But then installing a plugin means writing and loading a plist per script, and `rexd status` would have to ask launchd about each. The waiting logic would need a home too, because launchd would keep restarting scripts while Rex is down. One daemon also runs the same way under systemd.

## What was measured

These facts about Rex shaped the design. Each was measured against a real server, on Rex build 0.0.1793299046 with API revision 7. The tests in `e2e/` depend on most of them, so a Rex update that changes one of those fails a test. The `REX_SERVER` and `rex.log` rows are not covered, so check those by hand after a large Rex update.

| Behavior | Measured result | What rexd does |
| --- | --- | --- |
| `rex do script.lua --all` with no sessions | Exits 3, "block target default not found" | Waits for a session before starting scripts |
| The same with two or more sessions and no app connected | Exits 3, "ambiguous". With the app connected, rex uses its focused block. | Sets `REX_SESSION` |
| `--all` together with `--session` or `--block` | Refused: "use --all or --session, not both" | Uses `REX_SESSION`, which is allowed |
| The session named by `REX_SESSION` closes | The script keeps running and keeps hearing events | Nothing further |
| The server stops while a script runs | The script exits 4 about 2 seconds later, and it does not reconnect | Waits and starts it again |
| The server is not running and `--autostart=false` is set | `ls`, `do` and `config reload` exit 4 | Treats 4 as "Rex is not running" |
| The server is not running, for `config check` and `version` | Both work | Installs while Rex is down |
| `REX_SERVER` names a missing socket | **Silently uses the default server** | Removes it, and uses `-S`, which fails correctly |
| A Rex terminal's environment | Sets `REX_SERVER`, `REX_SESSION` and `REX_BLOCK` | Removes them, so rexd behaves the same everywhere |
| `SIGINT` to a script | Exits 130 | Treats any exit after a stop as a clean stop |
| A syntax or runtime error at the top of a script | Exits 1 | Counts it as a failure |
| A global table set in config code | Visible in files it requires | This is how `rexd.plugin` works |
| `require` through a symbolic link | Works | This is how `--link` keeps config code live |
| `rex.log` in config code | Not visible in the server's log | `rexd status` runs the check instead |
| `rex keymap --json` | Reports the server's real config path | Asks the server, because `--config` can change the path |
| `rex.exec` in a config action | Runs in the server's environment | Documented for plugin authors |

## Testing

Unit tests use a small fake `rex` that copies the exit statuses above. It is its own program, built without the race detector, because a race-enabled binary takes about a second to start on macOS.

End-to-end tests use a real Rex. Each starts `rex server run` with its own socket, state and config in a temporary folder, and checks that server's config path before doing anything else. They talk to it with `-S`, never `REX_SERVER`, which could fall back to the server you work in. The LaunchAgent tests use real launchd under labels of their own.
