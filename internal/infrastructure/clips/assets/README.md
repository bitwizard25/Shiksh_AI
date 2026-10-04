# Pre-recorded clips

Clips are WAV files (16-bit mono PCM) laid out as `<language>/<kind>.wav`, with numbered variants
such as `hi/filler_1.wav`, `hi/filler_2.wav` (one is picked at random). The kinds are `filler`
(at most 400 ms), `repeat`, `error` and `redirect`.

They are generated once per language with `voicecli assets` (Plan 2) and committed here. A missing
file only disables that clip: the tutor then stays silent where the clip would have played.
