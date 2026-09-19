# client-engineer

The client half of the [engineer](https://github.com/Pacenote-Sim/engineer) server plugin: a
Pacenote client plugin, compiled into the client by the team's server. It posts each lap's corners
to engineer after the last corner, plays engineer's line for each corner as the corner approaches,
fetches the radio line after a race lap, and shows the debrief when the stint is over.

It measures nothing and compares nothing. The client measures the corners; engineer compares them
with the team's laps; this carries one to the other and plays what comes back.

## What it does, event by event

| Event from the client | What it does |
|---|---|
| `stint.started` | forgets the previous stint's lines |
| `corner.passed`, Turn 1 | `POST /plugin/engineer/laps` with Turn 1 alone, the moment it is behind the car, so its line for the next lap is ready with a whole lap to spare whatever the straight's length. This report asks for no setup notes |
| `lap.last-corner` | `POST /plugin/engineer/laps` with the whole lap's corners, as measured. The lines come back for the next lap; the straight to the line pays for the answer. Engineer's store replaces the early report's lines with these |
| `sampled` | knows where the car is, for a late answer |
| `corner.approaching` | plays the line for that corner: from the previous lap's full answer, from its early Turn 1 answer, or from the lap before when both are late. With audio when the voice plugin wrote it, as words otherwise. Once per lap. If no answer is back yet, the line plays the moment one arrives, provided the apex is still ahead |

## What it compares against

Your best clean lap of the stint. Each corner is sent with the same corner from that lap beside it,
so a line can say "brake twenty metres later than your best" instead of "brake later". Corners are
matched by where they are on the lap, not by their number.

A teammate's lap, or the best in class, is not something a simulator publishes the telemetry for —
only lap times. A team reference has to come from another driver's own stint, through the server.

## With the sound off

Every lap posted says whether this client would play what it is sent. With nothing to hear — no
sound plugin, or the driver turned it off — engineer writes the lines and buys no audio for them,
and the driver reads them on this page.

## Which lines are heard

Engineer writes a line for every corner that lost time; the driver cannot hear them all when corners
come close together, and a line cut off by the next is worse than none. So the companion schedules:

- A line starts at the last moment that still leaves a second of silence before the driver has to
  brake, and not before: heard there, it is the freshest thing in their head at the corner. The cue
  comes up long before that, with room for the longest line a coach may write, and a shorter line
  waits on the straight. Braking points come from the previous lap; the time to them from the car's
  speed, checked again on every sample.
- A line plays only when nothing else is being spoken.
- A line held back marks its corner as waiting. Next lap, when a corner and the one after it cannot
  both be heard, the one that has waited longer is heard and the other waits. Two close corners
  alternate; nothing is cut and nothing is left out for ever.
- A corner too near to be spoken about at all is left out, silently, this lap.

The client raises a corner's cue up to twenty-five seconds of travel before its braking point, which
is room for a line of fifty words at the pace a voice reads, and never before the corner in front of it: on a circuit where the
corners come thick and fast the room is whatever the gap allows, and the coach is told how much that
is before it writes.
| `lap.completed` in a race | `GET /plugin/engineer/cues` for the radio line, and plays it |
| `stint.finished` | waits for engineer to write the debrief, `GET /plugin/engineer/debrief`, shows it on its page |

Its status line says what is happening: lines ready for the next lap, engineer has no key, the coach
did not answer, debrief ready. Its page shows the latest lines by corner, what the laps said about
the car, the radio, and the debrief with its findings and setup changes.

Two posts a lap means two of engineer's model calls a lap: the early one is small, one corner. That
was the choice made on 2026-09-18 over one call with a risk of Turn 1 arriving late on a short
straight.

## Sound

Playing is not this plugin's job: it hands audio and words to the client, which routes them to the
running plugin that plays — the voice companion. Without one, nothing is heard and the client's log
says so.

## Building and testing

`make` runs what CI runs. Engineer is a fake in every test, answering the way its README says.
`TESTING.md` has the rest.

## Licence

GNU General Public License, version 3 — see `LICENSE`. Like the client and engineer. The contract it
is built on (`github.com/pacenote-sim/clientplugin`) is Apache-2.0.
