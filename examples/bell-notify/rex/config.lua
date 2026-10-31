-- Config code. rexd loads this file into Rex's config.

-- Read this at the top of the file. It is only set while the file loads.
local plugin = rexd.plugin

rex.action{
  name = "bell_notify_test",
  title = "Bell Notify: Send a Test Notification",
  run = function()
    rex.exec{
      "osascript", "-e",
      string.format("display notification %q with title %q", "Version " .. plugin.version .. " is working", "Bell notify"),
    }
  end,
}

-- A plugin may bind keys. Keys in the user's init.lua win over this one.
rex.bind("ctrl+alt+shift+b", "bell_notify_test")
