package engineer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/clientplugin"
	"github.com/pacenote-sim/clientplugin/clientplugintest"
	"github.com/pacenote-sim/protocol/wire"

	engineer "github.com/pacenote-sim/client-engineer"
)

// fakeEngineer answers the three routes the way engineer's README says.
type fakeEngineer struct {
	mu       sync.Mutex
	reports  []map[string]any
	debriefs int
	noKey    bool
	refuse   bool
	broken   bool
}

func (f *fakeEngineer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, r *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rep)
		f.mu.Lock()
		f.reports = append(f.reports, rep)
		noKey, refuse, broken := f.noKey, f.refuse, f.broken
		f.mu.Unlock()
		switch {
		case noKey:
			http.Error(w, "no key", http.StatusServiceUnavailable)
			return
		case refuse:
			http.Error(w, "the coach did not answer", http.StatusBadGateway)
			return
		case broken:
			_, _ = w.Write([]byte("<html>"))
			return
		}
		lapNumber, _ := rep["lap"].(float64)
		lap := int(lapNumber)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stint_id": rep["stint_id"], "lap": lap, "mode": "cue.training",
			"cues": []map[string]any{
				{"turn": 1, "apex_pct": 250, "line": "Brake later into Turn 1.", "audio": []byte("MP3-T1"), "audio_type": "audio/mpeg"},
				{"turn": 3, "apex_pct": 800, "line": "Ease the throttle out of Turn 3."},
			},
			"setup_notes": []string{"The rear steps out on power."},
		})
	})
	mux.HandleFunc("GET /cues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stint_id": r.URL.Query().Get("stint"), "lap": 2, "mode": "cue.race",
			"cues": []map[string]any{{"turn": 0, "line": "Personal best. Car behind at eight tenths."}},
		})
	})
	mux.HandleFunc("GET /debrief", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.debriefs++
		n := f.debriefs
		f.mu.Unlock()
		if n < 3 {
			http.Error(w, "not yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stint_id":"s-1","track":"Demo Circuit","car":"Demo GT3","summary":"Steady, losing time in Turn 1.",
		 "findings":[{"area":"Turn 1","observation":"Braking 20 m early.","drill":"Brake at the 100 board for five laps."}],
		 "setup_changes":[{"setting":"Rear wing","from":"7","to":"6","because":"the car is stable enough to give some drag back"}]}`))
	})
	return mux
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("not in time")
}

func waitStatus(t *testing.T, h *clientplugintest.Host, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(last(h.Statuses()), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status never contained %q; statuses were %v", want, h.Statuses())
}

func last(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[len(ss)-1]
}

func newCompanion(t *testing.T, f *fakeEngineer) (*engineer.Companion, *clientplugintest.Host) {
	t.Helper()
	c, h, _ := newCompanionWithClock(t, f.handler())
	return c, h
}

// newCompanionWithClock is a companion whose clock the test moves: a played
// line keeps the speaker busy for its length, and only the clock frees it.
func newCompanionWithClock(t *testing.T, routes http.Handler) (*engineer.Companion, *clientplugintest.Host, func(time.Duration)) {
	t.Helper()
	h := clientplugintest.NewHost(t, routes)
	c := engineer.New()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	clock := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	c.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	require.NoError(t, c.Start(context.Background(), h))
	t.Cleanup(func() { _ = c.Stop() })
	return c, h, func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(d)
	}
}

func TestItIsAPlugin(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	m, err := clientplugin.LoadManifest(".")
	r.NoError(err)
	r.Equal(engineer.Name, m.Name)
	r.Equal(engineer.Name, m.ServerPlugin)
	r.Equal(clientplugin.KindCompanion, m.Kind)
	_, ok := clientplugin.Default.Companion(engineer.Name)
	r.True(ok, "init() registered it")
	r.Contains(engineer.New().Wants(), clientplugin.KindLapLastCorner)
}

func TestALapIsPostedAndItsLinesArePlayedOnTheNext(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	f := &fakeEngineer{}
	c, h, advance := newCompanionWithClock(t, f.handler())
	ctx := context.Background()
	at := time.Now()
	stint := clientplugin.Stint{ID: "s-1", Session: wire.SessionPractice, TrackLengthM: 3000, SetupOpen: true}
	corners := []clientplugin.Corner{{Turn: 1, ApexPct: 250, ApexKmh: 100, BrakeAtPct: 190, PeakBrakePct: 100}, {Turn: 2, ApexPct: 550}, {Turn: 3, ApexPct: 800}}

	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.Equal("waiting for the first lap", last(h.Statuses()))
	r.Contains(last(h.Pages()), "No lines yet")

	// The report leaves at the last corner, with the corners as measured and
	// nothing compared.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, ElapsedMs: 55000, Corners: corners}))
	waitFor(t, func() bool { return strings.HasPrefix(last(h.Statuses()), "2 lines ready") })
	r.Equal("2 lines ready for lap 2", last(h.Statuses()))
	f.mu.Lock()
	r.Len(f.reports, 1)
	rep := f.reports[0]
	f.mu.Unlock()
	r.Equal("s-1", rep["stint_id"])
	r.InDelta(1, rep["lap"], 0)
	r.Equal("practice", rep["session"])
	r.InDelta(3000, rep["track_length_m"], 0)
	r.Equal(true, rep["setup_open"])
	cs, _ := rep["corners"].([]any)
	r.Len(cs, 3)
	first, _ := cs[0].(map[string]any)
	r.InDelta(100, first["apex_kmh"], 0)
	r.InDelta(190, first["brake_at_pct"], 0)
	r.Nil(first["ref_apex_kmh"], "nothing compared")
	r.Nil(first["deficit_kmh"])
	r.Contains(last(h.Pages()), "Brake later into Turn 1.")
	r.Contains(last(h.Pages()), "The rear steps out on power.")

	// Next lap: Turn 1 has audio and is played; Turn 3 has words and is said;
	// Turn 2 has no line. Each once. The clock moves between corners, as the
	// car does: a spoken line keeps the speaker busy for its length.
	r.NoError(clientplugintest.Deliver(ctx, c,
		&clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 252},
		&clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 252},
	))
	advance(20 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c,
		&clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 2, ApexPct: 550},
		&clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 3, ApexPct: 798},
	))
	r.Equal([]clientplugintest.Clip{{Audio: []byte("MP3-T1"), ContentType: "audio/mpeg"}}, h.Played())
	r.Equal([]string{"Ease the throttle out of Turn 3."}, h.Said())

	// Lap 3 approaches before lap 2's answer is back: lap 1's lines serve.
	advance(20 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 3, Turn: 1, ApexPct: 250}))
	r.Len(h.Played(), 2, "last lap's Turn 1 line, rather than silence")
	advance(20 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 5, Turn: 1, ApexPct: 250}))
	r.Len(h.Played(), 2, "two laps late is too late")

	// A race lap fetches the radio and plays it.
	race := stint
	race.Session = wire.SessionRace
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{At: at, Stint: race, Lap: 2, LapMs: 60000}))
	waitFor(t, func() bool { return len(h.Said()) == 2 })
	r.Equal("Personal best. Car behind at eight tenths.", h.Said()[1])
	r.Contains(last(h.Pages()), "Radio")
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{At: at, Stint: stint, Lap: 2, LapMs: 60000}))
	r.Len(h.Said(), 2, "in practice there is no radio")

	// The stint ends: the debrief is looked for until it exists, then shown.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintFinished{At: at, Stint: stint, Laps: 2}))
	waitFor(t, func() bool { return last(h.Statuses()) == "debrief ready" })
	page := last(h.Pages())
	r.Contains(page, "Debrief · Demo Circuit")
	r.Contains(page, "Steady, losing time in Turn 1.")
	r.Contains(page, "<b>Turn 1</b>")
	r.Contains(page, "Brake at the 100 board")
	r.Contains(page, "<b>Rear wing</b> 7 → 6")
	f.mu.Lock()
	r.Equal(3, f.debriefs, "two looks found nothing, the third found it")
	f.mu.Unlock()
	r.NoError(c.Stop())
}

func TestWhatEngineerCannotDoIsSaidNotHidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	at := time.Now()
	stint := clientplugin.Stint{ID: "s-2", Session: wire.SessionPractice}
	lap := &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: []clientplugin.Corner{{Turn: 1, ApexPct: 100}}}
	ctx := context.Background()

	noKey, h := newCompanion(t, &fakeEngineer{noKey: true})
	r.NoError(clientplugintest.Deliver(ctx, noKey, lap))
	waitStatus(t, h, "no key")

	refusing, h2 := newCompanion(t, &fakeEngineer{refuse: true})
	r.NoError(clientplugintest.Deliver(ctx, refusing, lap))
	waitStatus(t, h2, "did not answer")

	broken, h3 := newCompanion(t, &fakeEngineer{broken: true})
	r.NoError(clientplugintest.Deliver(ctx, broken, lap))
	waitStatus(t, h3, "could not be read")
	r.NoError(broken.Stop())

	// Nothing to play for a corner nobody has lines for, and no host means no panic.
	quiet := engineer.New()
	r.NoError(quiet.Notify(ctx, &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1}))
	r.NoError(quiet.Notify(ctx, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1}))
	r.NoError(quiet.Stop())

	// A server plugin that has no such route: the status carries the number.
	h4 := clientplugintest.NewHost(t, nil)
	missing := engineer.New()
	r.NoError(missing.Start(context.Background(), h4))
	r.NoError(clientplugintest.Deliver(ctx, missing, lap))
	waitFor(t, func() bool { return last(h4.Statuses()) == "engineer refused lap 1: 404" })
	r.NoError(missing.Stop())

	// Stopped, then started again: the old state is gone with the stint.
	again, h5 := newCompanion(t, &fakeEngineer{})
	r.NoError(clientplugintest.Deliver(ctx, again, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.Equal("waiting for the first lap", last(h5.Statuses()))
	r.NoError(again.Stop())
}

func TestTheDebriefGivesUpPolitely(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	never := http.NewServeMux()
	never.HandleFunc("GET /debrief", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusNotFound) })
	h := clientplugintest.NewHost(t, never)
	c := engineer.New()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	r.NoError(c.Start(context.Background(), h))
	r.NoError(clientplugintest.Deliver(context.Background(), c, &clientplugin.StintFinished{At: time.Now(), Stint: clientplugin.Stint{ID: "s-3"}}))
	waitFor(t, func() bool { return last(h.Statuses()) == "no debrief was written for this stint" })
	r.Len(h.Requests(), engineer.DebriefTries)

	// A stop while waiting ends the wait.
	waiting := engineer.New()
	waiting.Sleep = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	r.NoError(waiting.Start(context.Background(), clientplugintest.NewHost(t, never)))
	r.NoError(clientplugintest.Deliver(context.Background(), waiting, &clientplugin.StintFinished{At: time.Now(), Stint: clientplugin.Stint{ID: "s-4"}}))
	r.NoError(waiting.Stop())

	// An answer that is not JSON, and a debrief the server refuses outright.
	odd := http.NewServeMux()
	odd.HandleFunc("GET /debrief", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	c2 := engineer.New()
	c2.Sleep = func(context.Context, time.Duration) error { return nil }
	h2 := clientplugintest.NewHost(t, odd)
	r.NoError(c2.Start(context.Background(), h2))
	r.NoError(clientplugintest.Deliver(context.Background(), c2, &clientplugin.StintFinished{At: time.Now(), Stint: clientplugin.Stint{ID: "s-5"}}))
	waitFor(t, func() bool { return strings.Contains(last(h2.Statuses()), "answered 403") })
	r.NoError(c2.Stop())
}

func TestTheEdgesOfTheRadioAndThePage(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Now()
	race := clientplugin.Stint{ID: "s-9", Session: wire.SessionRace}

	// No radio written for this lap: nothing is said and nothing breaks.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cues", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nothing yet", http.StatusNotFound) })
	mux.HandleFunc("GET /debrief", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stint_id":"s-9","summary":"","findings":[],"setup_changes":[{"setting":"Brake bias","to":"54.5","because":"locking the rears"}]}`))
	})
	h := clientplugintest.NewHost(t, mux)
	c := engineer.New()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	r.NoError(c.Start(ctx, h))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{At: at, Stint: race, Lap: 1, LapMs: 1}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintFinished{At: at, Stint: race}))
	waitStatus(t, h, "debrief ready")
	r.Empty(h.Said())
	page := last(h.Pages())
	r.Contains(page, "<h2>Debrief</h2>", "no track, no dot")
	r.Contains(page, "<b>Brake bias</b> 54.5 · locking the rears", "no from, no arrow")
	r.NotContains(page, "Lines from lap")
	r.NoError(c.Stop())
}

func TestAfterStopNothingReachesEngineer(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Now()
	race := clientplugin.Stint{ID: "s-10", Session: wire.SessionRace}
	h := clientplugintest.NewHost(t, (&fakeEngineer{}).handler())
	c := engineer.New()
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	r.NoError(c.Start(ctx, h))
	r.NoError(c.Stop())

	// The companion's context is gone: every request fails as unreachable,
	// which the status says, and nothing panics.
	r.NoError(clientplugintest.Deliver(ctx, c,
		&clientplugin.LapLastCorner{At: at, Stint: race, Lap: 1, Corners: []clientplugin.Corner{{Turn: 1, ApexPct: 100}}},
		&clientplugin.LapCompleted{At: at, Stint: race, Lap: 1, LapMs: 1},
		&clientplugin.StintFinished{At: at, Stint: race},
	))
	r.NoError(c.Stop())
	joined := strings.Join(h.Statuses(), " | ")
	r.Contains(joined, "engineer could not be reached")
	r.Contains(joined, "could not be reached for the debrief")
	r.Empty(h.Said())
}

// slowEngineer answers POST /laps only when told to, so the test controls
// when the lines arrive relative to the car.
type slowEngineer struct {
	release chan struct{}
}

func (s *slowEngineer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, r *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rep)
		<-s.release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stint_id": rep["stint_id"], "lap": rep["lap"], "mode": "cue.training",
			"cues": []map[string]any{{"turn": 1, "apex_pct": 250, "line": "Brake later into Turn 1."}},
		})
	})
	return mux
}

func TestALateAnswerStillPlaysTurnOneBeforeItsApex(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Now()
	stint := clientplugin.Stint{ID: "s-late", Session: wire.SessionPractice, TrackLengthM: 3000}
	corners := []clientplugin.Corner{{Turn: 1, ApexPct: 250}, {Turn: 2, ApexPct: 550}, {Turn: 3, ApexPct: 800}}

	slow := &slowEngineer{release: make(chan struct{})}
	c, h, advance := newCompanionWithClock(t, slow.handler())
	sample := func(lap int, pct float64) *clientplugin.Sampled {
		return &clientplugin.Sampled{Stint: stint, Lap: lap, Sample: clientplugin.Sample{At: at, LapDistPct: pct}}
	}

	// Lap 1's report leaves at its last corner; the answer is slow. The car
	// crosses the line and Turn 1 of lap 2 comes into range: nothing to play yet.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: corners}))
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.20), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 250, MetresToApex: 150}))
	r.Empty(h.Said())

	// The car is at 22 %, apex at 25 %: the answer arrives and the line plays now.
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.22)))
	close(slow.release)
	waitFor(t, func() bool { return len(h.Said()) == 1 })
	r.Equal("Brake later into Turn 1.", h.Said()[0])
	waitStatus(t, h, "1 lines ready for lap 2")

	// Turn 1 comes round again on lap 3 with lap 2's answer late too; the car
	// has already passed the apex when it arrives: the line is not played
	// behind the car. Lap 1's line serves at the approach instead.
	slow.release = make(chan struct{})
	advance(30 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 2, Corners: corners}))
	r.NoError(clientplugintest.Deliver(ctx, c, sample(3, 0.20), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 3, Turn: 1, ApexPct: 250}))
	r.Len(h.Said(), 2, "lap 1's line served while lap 2's answer was late")
	r.NoError(clientplugintest.Deliver(ctx, c, sample(3, 0.30)))
	close(slow.release)
	waitStatus(t, h, "1 lines ready for lap 3")
	r.Len(h.Said(), 2, "the apex was behind the car: nothing more")
}

func TestTurnOneIsPostedEarlyAndReadyAWholeLapAhead(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Now()
	stint := clientplugin.Stint{ID: "s-early", Session: wire.SessionPractice, TrackLengthM: 3000, SetupOpen: true}
	t1 := clientplugin.Corner{Turn: 1, ApexPct: 250, ApexKmh: 100}
	corners := []clientplugin.Corner{t1, {Turn: 2, ApexPct: 550}, {Turn: 3, ApexPct: 800}}

	f := &fakeEngineer{}
	c, h, advance := newCompanionWithClock(t, f.handler())
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))

	// Turn 1 passed on lap 1: a report with Turn 1 alone leaves at once, and
	// says nothing about the setup so that engineer notes the car only once.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerPassed{At: at, Stint: stint, Lap: 1, Corner: t1}))
	waitStatus(t, h, "Turn 1 ready for lap 2")
	f.mu.Lock()
	r.Len(f.reports, 1)
	early := f.reports[0]
	f.mu.Unlock()
	cs, _ := early["corners"].([]any)
	r.Len(cs, 1)
	r.Nil(early["setup_open"], "the early report asks for no setup notes")
	r.NotContains(last(h.Pages()), "Lines from lap", "the page waits for the full answer")

	// Turns 2 and 3 pass: nothing more is posted until the last corner.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerPassed{At: at, Stint: stint, Lap: 1, Corner: corners[1]}))
	f.mu.Lock()
	r.Len(f.reports, 1)
	f.mu.Unlock()

	// Turn 1 of lap 2 comes into range before the full answer exists: the
	// early line plays.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 250}))
	r.Equal([]clientplugintest.Clip{{Audio: []byte("MP3-T1"), ContentType: "audio/mpeg"}}, h.Played())

	// The full report at the last corner: two posts for lap 1 in all, and the
	// full answer's lines serve the rest of lap 2.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: corners}))
	waitStatus(t, h, "2 lines ready for lap 2")
	f.mu.Lock()
	r.Len(f.reports, 2)
	r.Equal(true, f.reports[1]["setup_open"])
	f.mu.Unlock()
	advance(20 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 3, ApexPct: 800}))
	r.Equal([]string{"Ease the throttle out of Turn 3."}, h.Said())
}

// Two corners close together: the first line plays, the second is held back
// because the speaker is busy or the words would not end before braking; on
// the next lap the held-back corner has waited longer and gets its turn.
func TestCloseCornersTakeTurnsAcrossLaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	stint := clientplugin.Stint{ID: "s-close", Session: wire.SessionPractice, TrackLengthM: 3000}
	// Turn 1 brakes at 200 ‰, Turn 2 at 260 ‰: 180 m apart, under three seconds at 250 km/h.
	corners := []clientplugin.Corner{{Turn: 1, ApexPct: 230, BrakeAtPct: 200}, {Turn: 2, ApexPct: 290, BrakeAtPct: 260}, {Turn: 3, ApexPct: 800, BrakeAtPct: 760}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, req *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(req.Body).Decode(&rep)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stint_id": rep["stint_id"], "lap": rep["lap"], "cues": []map[string]any{
				{"turn": 1, "apex_pct": 230, "line": "Turn 1, brake twenty metres later and keep the car straight."},
				{"turn": 2, "apex_pct": 290, "line": "Turn 2, ease off the brake before the apex."},
				{"turn": 3, "apex_pct": 800, "line": "Turn 3, pick up the throttle earlier."},
			},
		})
	})
	c, h, advance := newCompanionWithClock(t, mux)
	sample := func(lap int, pct float64, kmh float64) *clientplugin.Sampled {
		return &clientplugin.Sampled{Stint: stint, Lap: lap, Sample: clientplugin.Sample{At: at, LapDistPct: pct, SpeedKmh: kmh}}
	}
	lapAnswer := func(lap int) {
		r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: lap, Corners: corners}))
		waitStatus(t, h, "3 lines ready for lap "+strconv.Itoa(lap+1))
	}

	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	lapAnswer(1)

	// Lap 2 at 250 km/h. Turn 1's cue comes up 540 m before its braking point
	// and its eleven words need every metre of it, so it starts at once. Turn
	// 2's comes up while Turn 1's is still being spoken.
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.02, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 230}))
	r.Len(h.Said(), 1)
	advance(2 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.08, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 2, ApexPct: 290}))
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.10, 250)))
	r.Len(h.Said(), 1, "Turn 2's line held back: Turn 1's is still being spoken")
	// Turn 3's seven words need less room than the cue gives them, so the
	// line waits on the straight and starts when it has to.
	advance(20 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.58, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 3, ApexPct: 800}))
	r.Len(h.Said(), 1, "540 m from braking, with seven words to say, is too early")
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.63, 250)))
	r.Len(h.Said(), 2)

	// Lap 3: Turn 2 has waited a lap, Turn 1 has not, and the two cannot both
	// be heard: Turn 1 gives way this time, Turn 2 is heard.
	lapAnswer(2)
	advance(30 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(3, 0.02, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 3, Turn: 1, ApexPct: 230}))
	r.Len(h.Said(), 2, "Turn 1 held back so that Turn 2 is heard this lap")
	advance(2 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(3, 0.08, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 3, Turn: 2, ApexPct: 290}))
	r.NoError(clientplugintest.Deliver(ctx, c, sample(3, 0.10, 250)))
	r.Len(h.Said(), 3)
	r.Contains(h.Said()[2], "Turn 2")

	// Lap 4: back to Turn 1.
	lapAnswer(3)
	advance(30 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(4, 0.02, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 4, Turn: 1, ApexPct: 230}))
	r.Len(h.Said(), 4)
	r.Contains(h.Said()[3], "Turn 1")

	// A line that cannot end before the braking point is not started at all.
	advance(30 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(4, 0.755, 250), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 4, Turn: 3, ApexPct: 800}))
	r.Len(h.Said(), 4, "15 m from braking: too late to say anything")
}

// A line the coach wrote for two corners too close for a line each comes
// filed under the first. The second corner matches it by distance, and the
// driver hears it once, before the first.
func TestAJoinedLineIsHeardOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	stint := clientplugin.Stint{ID: "s-joined", Session: wire.SessionPractice, TrackLengthM: 3000}
	corners := []clientplugin.Corner{{Turn: 1, ApexPct: 230, BrakeAtPct: 200}, {Turn: 2, ApexPct: 260, BrakeAtPct: 245}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, req *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(req.Body).Decode(&rep)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stint_id": rep["stint_id"], "lap": rep["lap"], "cues": []map[string]any{
				{"turn": 1, "apex_pct": 230, "line": "Turns 1 and 2, brake once and carry the speed."},
			},
		})
	})
	c, h, advance := newCompanionWithClock(t, mux)
	sample := func(lap int, pct float64) *clientplugin.Sampled {
		return &clientplugin.Sampled{Stint: stint, Lap: lap, Sample: clientplugin.Sample{At: at, LapDistPct: pct, SpeedKmh: 200}}
	}

	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: corners}))
	waitStatus(t, h, "1 lines ready for lap 2")

	// The cue comes up early, with room for a line far longer than this one,
	// so the line waits: nothing is said at 20 ‰.
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.02), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 1, ApexPct: 230}))
	r.Empty(h.Said(), "540 m from the braking point, with nine words to say, is too early")
	// Closer in, the moment comes and the line is spoken.
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.08)))
	r.Equal([]string{"Turns 1 and 2, brake once and carry the speed."}, h.Said())
	advance(10 * time.Second)
	r.NoError(clientplugintest.Deliver(ctx, c, sample(2, 0.235), &clientplugin.CornerApproaching{At: at, Stint: stint, Lap: 2, Turn: 2, ApexPct: 260}))
	r.Len(h.Said(), 1, "the joined line was heard again before its second corner")
}

// What a sample costs. Every reading the app takes reaches this companion,
// because it is what decides when a line starts, so it runs at the simulator's
// own rate beside everything else.
func BenchmarkASample(b *testing.B) {
	c := engineer.New()
	h := clientplugintest.NewHost(b, nil)
	if err := c.Start(context.Background(), h); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Stop() })
	stint := clientplugin.Stint{ID: "s-bench", Session: wire.SessionPractice, TrackLengthM: 3650}
	e := &clientplugin.Sampled{
		Stint: stint, Lap: 3,
		Sample: clientplugin.Sample{At: time.Now(), LapDistPct: 0.42, SpeedKmh: 180},
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := c.Notify(ctx, e); err != nil {
			b.Fatal(err)
		}
	}
}

// And what comparing a lap costs: every corner of the lap matched against the
// same corner of the best lap, once a lap, before it is posted.
func BenchmarkComparingALap(b *testing.B) {
	lap := make([]clientplugin.Corner, 0, 16)
	best := make([]clientplugin.Corner, 0, 16)
	for i := range 16 {
		lap = append(lap, clientplugin.Corner{Turn: i + 1, ApexPct: i * 60, ApexKmh: 80 + i, BrakeAtPct: i*60 - 20})
		best = append(best, clientplugin.Corner{Turn: i + 1, ApexPct: i*60 + 3, ApexKmh: 85 + i, BrakeAtPct: i*60 - 14})
	}
	b.ReportAllocs()
	for b.Loop() {
		if got := engineer.Compare(lap, best); len(got) != len(lap) {
			b.Fatal("a corner went missing")
		}
	}
}
