# Headless settings menu

Run `nexus settings --config /path/to/config.yaml` (alias `nexus config menu`).
Use the exact configuration path supplied to the host's service. Without the flag,
the menu targets `~/.NexusRouter/config/config.yaml`; it does not guess a running
service's file. It works offline without a browser, API session, or running daemon.

Choose numbered sections and fields. Press Enter to open a section, `b` to go back,
`s` to validate and save, or `q` to discard. EOF discards edits. Optional sections
can be disabled with `null` at their section prompt. Strings accept plain text;
numbers, booleans, lists and objects accept JSON. Lists replace the entire list,
including providers and models. Use the configuration reference for their fields.
All fields in the configuration schema are exposed automatically.

Values are hidden in the menu. `nexus config show --config PATH` provides the existing
redacted configuration view. Terminal input is echoed: use credential references,
not credential values. The editor starts from the selected file plus defaults and
does not persist environment overrides or settings from another configuration file.

Saving validates the complete draft with the normal configuration loader. Invalid
changes remain staged. Saves create a private backup, detect intervening file edits,
and atomically replace the file with mode 0600. Comments/formatting are normalized;
the original is preserved in the backup. Concurrent menu saves use an exclusive
`.menu.lock`; a lock left by a crash must be removed after confirming no editor is
saving. External writers should not edit this file concurrently.

Configuration editing does not start/stop services, pair peers, install privileged
DNS capture, or restart routers. Apply startup changes at an idle boundary using
the host's existing service workflow. Runtime operations remain separate CLI commands.
