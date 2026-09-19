# Testing the engineer companion

`make` runs what CI runs, in order: format, build, vet, lint, the suite with the race detector and
shuffled order, coverage over 90 %, every benchmark once, and `go mod tidy` a no-op.

```
make            # everything, in order
make test       # the suite alone
make cover      # the suite with the coverage floor
make bench      # what a sample and a lap cost
```

## What the suite asserts

| | |
|---|---|
| the lap it posts | at the last corner, with the corners compared against the best lap of the stint, and Turn 1 alone the moment it is passed so its line is ready a lap early |
| the comparison | corners matched by where they are on the lap and not by number, a corner that gained left uncompared, a slower lap not becoming the reference, and a lap that was not clean never becoming it |
| which lines are heard | a line that fits, a line held because the speaker is busy, a line held because the next corner has waited longer, and two close corners taking turns across laps |
| when a line starts | held on the straight and started at the last moment that still ends before the braking point, checked again on every sample |
| a joined line | one line for two corners too close for one each, heard once, before the first of them |
| what it says to engineer | the early flag, whether this client would play what it is sent, and the driver's own best lap as the reference |
| the answers | a lap's lines filed and spoken, a late answer still played if the apex is ahead, the radio in a race, and the debrief after the stint |

Everything runs against a fake of engineer's routes, written from that plugin's README. The app is
not needed and neither is a server.

The clock is movable: `newCompanionWithClock` hands the test a function that advances it, so a rule
about what fits in eight seconds is tested in eight microseconds.

## What it costs

| | |
|---|---|
| a sample | 12 ns, no allocations |
| comparing a lap of sixteen corners | 1.5 µs, one allocation |

A sample reaches this companion at the simulator's rate, which is why it does nothing on most of
them: it keeps the position and the speed, and looks at whether a held line is due.
