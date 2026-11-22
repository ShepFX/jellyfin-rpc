# Jellyfin RPC: Discord status for Jellyfin Desktop

Shows what you're watching or listening to in **Jellyfin Desktop** as your Discord status ("Watching Rick and Morty", with poster, episode, progress bar and IMDb/TMDB buttons).

It's a drop-in mod. Two files go next to Jellyfin Desktop, and it runs inside the app: when Jellyfin Desktop starts, your Discord status starts; when it closes, it stops. Nothing else to run, and no server plugin. It works with any Jellyfin server.

## Install

1. Quit Jellyfin Desktop completely, including from the system tray.
2. Open its program folder: right-click the shortcut, then **Open file location**. It's the folder with `Jellyfin Desktop.exe`; for the portable version, the folder you unzipped.
3. Copy `version.dll` and `jellyfinrpc_core.dll` into that folder.
4. Start Jellyfin Desktop. The first time, a box shows a **Quick Connect code**. In Jellyfin, open your profile picture, then **Settings**, then **Quick Connect**, enter the code and press **Authorize**. You only do this once.

Play something and check your Discord profile. The Discord desktop app must be running; the browser version can't show statuses.

## What it shows

| Playing | Status |
| --- | --- |
| Episode | **Watching** Show name · S2E5 · Episode title, show poster |
| Movie | **Watching** Movie title · year, poster |
| Music | **Listening to** Track · artists, album cover |

- Posters come from TMDB, through the artwork lookup Jellyfin already does. Music covers come from iTunes. Image links never point at your server, so your server address is never shown on Discord.
- The progress bar follows pauses and seeking. While paused it shows "Paused", and the status hides after 5 minutes paused.
- It only shows playback in Jellyfin Desktop on this PC, not your TV or phone.

## Settings

Settings are in `%APPDATA%\jellyfin-rpc\config.json`; edit it with Jellyfin Desktop closed. Everything is optional:

| Setting | Default | |
| --- | --- | --- |
| `show_movies`, `show_episodes`, `show_music` | `true` | turn types on or off |
| `show_buttons` | `true` | IMDb / TMDB buttons (others see them, you don't) |
| `hide_paused_after_minutes` | `5` | `0` = keep showing while paused |
| `server` | from Jellyfin Desktop | set it if you want to override the server address |
| `discord_client_id` | the "Jellyfin" app | use your own Discord application instead |

To sign in again, delete `config.json`. The log is `%APPDATA%\jellyfin-rpc\jellyfin-rpc.log`.

## Uninstall

Quit Jellyfin Desktop and delete the two DLLs. Optionally delete `%APPDATA%\jellyfin-rpc`.

## Notes

- **Quick Connect** must be enabled on the server, which is the default. If it's off, a server admin can turn it on in Dashboard, General, Quick Connect.
- **Antivirus:** a `version.dll` next to a program is a pattern some antivirus tools flag, because malware uses the same trick. The files are unsigned. If it stops working, check your antivirus quarantine.
- **Updates:** a Jellyfin Desktop update may remove the files. Copy them back.
- **If Jellyfin Desktop won't start**, delete the two files and it's back to normal. The proxy forwards every call to Windows' real `version.dll`.
- Your sign-in token is stored encrypted for your Windows user (DPAPI).

## Build

On Windows, with Go and [llvm-mingw](https://github.com/mstorsjo/llvm-mingw): `powershell -File build.ps1 -Version 1.0.0`. The output goes in `dist\`.

## Credits

The `version.dll` proxy and Discord IPC client are adapted from [NotHGM/plex-rpc](https://github.com/NotHGM/plex-rpc) (MIT). The default Discord application is the public "Jellyfin" app used by [jellyfin-rpc](https://github.com/Radiicall/jellyfin-rpc).
