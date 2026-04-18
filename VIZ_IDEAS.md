# Visualizer ideas

Running list of candidate visualizers for the bottom-right pane. Press `v` to
cycle, `V` to pick. Aim: ambient, colorblind-friendly (avoid yellow/green
adjacency), pleasant background chrome.

## Shipped

- **Plasma** — four overlapping sine waves, synthwave palette. Default.
- **Life** — Conway's Game of Life, toroidal wrap, cells age to bright.
- **Stars** — radial warp starfield.
- **Constellation** — repos as named stars, dirty/unpushed brighten.
- **Boids** — flocking particles + 2 predators that scatter the flock.
- **Clock** — digital big-digit wall clock + pomodoro (4×25 work / 5 break,
  15 long break every 4th). Keys when Clock is active: `p` start/pause,
  `R` reset, `.` skip phase.
- **Orbital** — repos as planets orbiting a central star, faint orbit rings,
  active repos glow pink, outer orbits rotate slower.
- **Rain** — diagonal streaks + occasional lightning; density scales with
  how many repos are dirty/unpushed/behind.

## Queued

### Abstract / ambient

- **Fire** — doom-style flame buffer, upward cooling. Avoid yellow band; use
  magenta→pink→orange instead.
- **Waveform** — oscilloscope driven by hash of recent commits.
- **Moiré** — two rotating line grids interfering.
- **Mandelbrot / Julia** — slowly zooming fractal, palette cycling.
- **Matrix rain** — falling glyph columns.
- **Tide pools** — Perlin-noise contour map that breathes.

### Data-reflective (like Constellation)

- **Pulse monitor** — EKG line per repo, spikes on commits / unpushed.
- **Weather map** — repos as regions, storms where dirty/behind, clear where
  clean.
- **Skyline** — bar-chart city, building height = commits, lit windows =
  unpushed.
- **Tree rings** — concentric rings per repo, new ring per day, color by
  activity.
- **Subway map** — branches as colored lines with station dots at commits.

### Interactive / informational

- **Sparkline grid** — tiny per-repo commit sparklines. Closest to "useful".
- **Pomodoro** — shipped as part of Clock.
