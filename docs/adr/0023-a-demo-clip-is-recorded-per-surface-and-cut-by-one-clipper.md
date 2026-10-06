# ADR-0023: A demo clip is recorded per surface and cut by one clipper

`kind = "clip"` records a looping demo. Recorders split by the surface they capture (terminal, macOS window, iOS simulator, web page), not by archetype: a raylib game and a SwiftUI app are both a macOS window. One clipper serves every recorder. It finds the seam by changed-pixel percentage, cuts exactly one period by frame index, crossfades only a seam that differs, and encodes WebP under a budget.

What the demo shows is the project's. It is written either as `[clip].steps` in the recorder's own words, or as an in-app demo that starts on `ADMIN_CLIP_START` and repeats every `period`. The second exists for what no outside driver reaches: hover, a game canvas, typing one character at a time.
