# Recording a demo clip

`kind = "clip"` records a looping demo of a project's app and writes it as an animated WebP. A
project names the command, and the `[clip]` table says how its app is recorded:

```toml
[commands.noodle-clip]
desc  = "Record the demo loop and post it"
steps = ["clip"]

[actions.clip]
kind = "clip"

[clip]
recorder = "terminal"
launch   = "admin"
ready    = "Commands"          # regex on screen before recording starts
period   = 4.8                 # the clip is exactly one period
warmup   = 0.5
budget   = "2MB"
publish  = 'python3 ~/Projects/portfolio/scripts/noodle.py edit admin --file "$ADMIN_CLIP_FILE"'
steps    = ["Down", "Sleep 750ms", "Down", "Sleep 750ms", "Down", "Sleep 750ms",
            "Up", "Sleep 750ms", "Up", "Sleep 750ms", "Up", "Sleep 750ms"]

[clip.terminal]
width_px  = 1920
height_px = 1080
font_size = 28
theme     = "Catppuccin Mocha"
```

`admin noodle-clip --no-publish` skips `publish` for one run.

## What a run does

```text
recorder: launch, wait for ready, record warmup + repeat × period of the steps
clipper:  decode the take to 160×90 gray frames at 30 fps
          pick the cut start within ±0.5 s of warmup whose frame best matches the frame one period later
          crossfade `fade` s of what follows the period into its start when more than seam_threshold % of pixels differ
          crop, scale to `width`, write clip.mp4 and stills/still-0..4.png (still-4 beside still-0 is the seam)
          encode clip.webp at `fps`, stepping quality down by 15 until it fits `budget`; fail below 25
publish:  run `publish` with ADMIN_CLIP_FILE set
```

Everything lands in `out` (default `$TMPDIR/admin-clip/<project>/clip`). `clip.log` there holds
every subprocess command line, its exit status and its stderr. `clip.json` holds the run's
result: cut start, seam difference, crossfade, length, size, every quality tried and any
error. The tool writes it even when the run fails.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `recorder` | — | The surface: `terminal`, `macos-window` or `web` |
| `launch` | — | Starts the app |
| `ready` | — | Regex the terminal recorder waits for before recording |
| `web.url` | — | web: the page; `$ADMIN_CLIP_PORT` is replaced with the port handed to `launch` |
| `web.width` / `web.height` / `web.scale` | 1280 / 720 / 2 | web: viewport in CSS pixels and device pixel ratio |
| `web.scheme` | none | web: emulate `prefers-color-scheme`, `dark` or `light` |
| `web.browser` | installed Chrome | web: a Chrome or chrome-headless-shell binary |
| `web.timeout` | 180 | web: seconds to wait for the server and for `window.__clip` |
| `macos.owner` | — | macos-window: the window owner name (the app's name) |
| `macos.title` | none | macos-window: text the window title must contain |
| `macos.settle` / `macos.timeout` | 1 / 60 | macos-window: seconds the bounds must hold still / longest wait for the window |
| `period` | — | Loop length, seconds |
| `warmup` | 0 | Seconds recorded before the nominal cut start |
| `repeat` | 2 | Times the steps run |
| `steps` | — | The choreography, in the recorder's own words |
| `fade` | 0.5 | Crossfade seconds when the seam differs |
| `seam_threshold` | 0.5 | Percentage of changed pixels that counts as a differing seam |
| `width` / `fps` / `quality` | 1280 / 15 / 70 | Output size, frame rate, first WebP quality |
| `budget` | none | Largest WebP allowed, e.g. `2MB` |
| `crop` | none | ffmpeg crop `w:h:x:y` applied before scaling |
| `out` | `$TMPDIR/admin-clip/<project>/clip` | Output directory |
| `publish` | none | Shell command run after a passing clip |

`table = "<name>"` on the action reads `[<name>]` instead of `[clip]`, for a project with more
than one clip.

## Choreography

Write `steps` in the recorder's own words. For `terminal` that means vhs tape commands such as
`Down`, `Enter`, `Type "q"` and `Sleep 500ms`, typed into vhs's own headless terminal, so no
keystroke reaches your keyboard or focus. Make the steps end where they began and the seam
needs no crossfade.

When steps can't reach what the demo needs (hover, a game canvas, typing one character at a
time), the app runs its own demo. It does so when it sees `ADMIN_CLIP=1`. Its timeline starts
when the file named by `ADMIN_CLIP_START` appears, and the demo repeats every `period` seconds.

## macOS windows

`recorder = "macos-window"` records one window of a native app, a raylib game included. `launch`
starts the app and gets `ADMIN_CLIP=1` and `ADMIN_CLIP_START` in its environment. `open` does
not forward the environment, so pass both through `open --env`:

```toml
[clip]
recorder = "macos-window"
launch   = 'open -g -n --env ADMIN_CLIP=1 --env ADMIN_CLIP_START="$ADMIN_CLIP_START" "mac/build/NTS Radio.app"'
period   = 5.0
warmup   = 0.5

[clip.macos]
owner = "NTS Radio"
```

The recorder runs these steps:
1. It lists windows through `osascript -l JavaScript`.
2. It waits for a window owned by `owner` from a process that did not exist before launch, so a
   copy of the app you already have open is never recorded.
3. It waits until the window's bounds hold still for `settle` seconds.
4. It records with `screencapture -v -x -o -l<id>`: the window's pixels at its backing scale,
   with no shadow, even when something covers the window.
5. It touches `ADMIN_CLIP_START` 1.5 s into the recording. That moment is t0.
6. It runs `steps`, then stops `screencapture` with SIGINT and quits the app it launched.
`screencapture`'s first frame lags its launch. The recorder measures that lag as wall time minus
video length, and subtracts it from t0.

`steps` here are `sh <command>` and `wait <seconds>`. Hover, typing and anything drawn on a
canvas belong in the app's own demo. The terminal running `admin` needs Screen Recording
permission.

## Web pages

`recorder = "web"` renders a page frame by frame in headless Chrome, driven from Go over the
DevTools protocol, so no browser window ever shows.

```toml
[clip]
recorder = "web"
launch   = 'exec uv run plexdb explore --host 127.0.0.1 --port "$ADMIN_CLIP_PORT"'
period   = 4.5

[clip.web]
url    = "http://127.0.0.1:$ADMIN_CLIP_PORT/?clip"
scheme = "dark"
```

The recorder runs these steps:
1. It picks a free port and, when `launch` is set, starts it in its own process group, with `ADMIN_CLIP=1` and
   `ADMIN_CLIP_PORT` set. Write the port as `$ADMIN_CLIP_PORT`, without braces, so the shell
   expands it rather than `admin`.
2. It waits for `url` to answer.
3. It opens `url` at the configured viewport and waits for the page to expose
   `window.__clip = { period, seek(t) }`. `seek(t)` draws the frame at `t` seconds and resolves
   once that frame is painted. `t` and `t + period` draw the same frame.
4. It refuses the page when `window.__clip.period` differs from `[clip].period`.
5. For each frame `i` it awaits `seek(i / 30)` and takes a screenshot. The take is therefore
   exact to the frame, and the seam needs no search.
6. It stops the launch's process group: SIGTERM, then SIGKILL after 5 s.

Leave `launch` unset to record a page that is already being served. `web.timeout` bounds the
server wait, the page load and the wait for `window.__clip`, each on its own. Each `seek`
gets 30 s. `web.width × web.scale` and `web.height × web.scale` must come out as even whole
numbers of pixels.

The recorder writes the page's console lines and exceptions to `clip.log`. A page with no
`window.__clip` is refused, because this recorder renders only seekable pages.
