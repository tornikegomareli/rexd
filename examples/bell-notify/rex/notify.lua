-- Event script. The rexd daemon keeps this running and restarts it if it exits.
-- It must register at least one handler, because a script with none exits at once.

rex.on("terminal.bell", function(ctx, ev)
  -- Called without a session_id, this describes the session the bell rang in.
  local view = rex.session.view{}
  local where = view and view.label or "a session"
  rex.exec{
    "osascript", "-e",
    string.format("display notification %q with title %q", "Bell in " .. where, "Rex"),
  }
end)

print("listening for terminal bells")
