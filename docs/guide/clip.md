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
| `recorder` | — | The surface: `terminal` |
| `launch` | — | Starts the app |
| `ready` | — | Regex the terminal recorder waits for before recording |
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
