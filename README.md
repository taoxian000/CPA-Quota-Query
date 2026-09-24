# Codex Quota Tray Monitor

A small Windows tray monitor for the Codex quota endpoint. It uses Go's
standard library and Win32 APIs, with no third-party packages or runtime
installation required on the target PC.

## Build

Run build.bat with Go installed. It creates quota-monitor.exe in this
folder. The executable is a Windows GUI program, so it does not open a console.

Pushing any Git tag runs the GitHub Actions workflow, which tests the project
and builds a Windows AMD64 executable. The executable is published as a
workflow artifact.

## Use

- The tray icon draws the actual account's remaining 5-hour and weekly quota.
- Left-click the icon to show or hide the wider panel.
- Drag the panel with the left mouse button; its position is saved and restored
  on subsequent openings and launches.
- The nominal-account card stacks its name and quota bars above the reset-credit
  section, which shows every returned expiry time and remaining time. The reset
  button asks for confirmation, consumes
  one credit for each account covered by the server's nominal prefix, displays
  the server result, and then refreshes the panel. A timed-out reset is not
  retried automatically because its final server-side state may be uncertain.
- The panel displays both nominal and actual account names and quota bars. Its
  title bar has a light/dark/follow-system theme switch, an always-on-top toggle,
  minimize, and exit buttons; right-clicking the panel opens the tray menu.
- Right-click to refresh, open the window once, toggle always-on-top, enable
  startup at Windows sign-in, or exit.
- The app refreshes every 10 minutes by default. Right-click the tray icon to
  choose 10 minutes, 20 minutes, 30 minutes, or 1 hour; the choice is saved.
- The app reads the auth.json file under the current user's .codex folder for
  OPENAI_API_KEY.
- The selected panel options are stored under the current user's app config
  directory. The startup option uses the current user's Windows Run registry key.

All quota bars transition continuously from red through blue to green as the
remaining percentage increases.
