package engineer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/clientplugin"
	"github.com/pacenote-sim/clientplugin/clientplugintest"
	"github.com/pacenote-sim/protocol/wire"

	engineer "github.com/pacenote-sim/client-engineer"
)

// The client has both laps, so the client compares: every report after the
// first carries the same corner from the driver's best lap of the stint beside
// the one just driven, which is what lets the coach say where to brake rather
// than only what happened.
func TestALapIsComparedWithTheBestSoFar(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	stint := clientplugin.Stint{ID: "s-cmp", Session: wire.SessionPractice, TrackLengthM: 3650}

	posted := make(chan map[string]any, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, req *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(req.Body).Decode(&rep)
		posted <- rep
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stint_id": rep["stint_id"], "lap": rep["lap"], "cues": []any{}})
	})
	c, h, _ := newCompanionWithClock(t, mux)

	fast := []clientplugin.Corner{
		{Turn: 1, ApexPct: 100, ApexKmh: 92, MinKmh: 90, ExitKmh: 130, BrakeAtPct: 70, ThrottleLag: 20, GearAtApex: 3},
		{Turn: 2, ApexPct: 500, ApexKmh: 78, MinKmh: 76, ExitKmh: 118, BrakeAtPct: 470, ThrottleLag: 15, GearAtApex: 2},
	}
	slow := []clientplugin.Corner{
		{Turn: 1, ApexPct: 104, ApexKmh: 85, MinKmh: 80, ExitKmh: 120, BrakeAtPct: 60, ThrottleLag: 45, GearAtApex: 2},
		{Turn: 2, ApexPct: 498, ApexKmh: 80, MinKmh: 79, ExitKmh: 121, BrakeAtPct: 476, ThrottleLag: 10, GearAtApex: 2},
	}

	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))

	// The first lap has nothing to compare with, and says so by saying nothing.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: slow}))
	first := <-posted
	_, named := first["reference"]
	r.False(named, "the first lap of a stint has no reference")
	corners, _ := first["corners"].([]any)
	one, _ := corners[0].(map[string]any)
	_, compared := one["ref_apex_kmh"]
	r.False(compared)

	// Lap 1 completes clean and becomes the reference.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{
		At: at, Stint: stint, Lap: 1, LapMs: 98000, LapKind: wire.KindClean, Corners: fast,
	}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 2, Corners: slow}))
	second := <-posted
	r.Equal(engineer.ReferenceName, second["reference"])
	corners, _ = second["corners"].([]any)
	r.Len(corners, 2)

	// Turn 1: seven km/h down at the apex, braking earlier, later on the power.
	one, _ = corners[0].(map[string]any)
	r.EqualValues(85, one["apex_kmh"])
	r.EqualValues(92, one["ref_apex_kmh"])
	r.EqualValues(7, one["deficit_kmh"])
	r.EqualValues(60, one["brake_at_pct"])
	r.EqualValues(70, one["ref_brake_at_pct"], "the braking point of the best lap, for the coach to measure against")
	r.EqualValues(45, one["throttle_lag"])
	r.EqualValues(20, one["ref_throttle_lag"])
	r.EqualValues(90, one["ref_min_kmh"])
	r.EqualValues(130, one["ref_exit_kmh"])
	r.EqualValues(3, one["ref_gear_at_apex"])

	// Turn 2 was quicker than the reference: compared, but no deficit.
	two, _ := corners[1].(map[string]any)
	r.EqualValues(78, two["ref_apex_kmh"])
	_, lost := two["deficit_kmh"]
	r.False(lost, "a corner that gained is not a corner that lost time")

	// A slower lap does not replace the reference; a faster one does.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{
		At: at, Stint: stint, Lap: 2, LapMs: 99500, LapKind: wire.KindClean, Corners: slow,
	}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 3, Corners: slow}))
	third := <-posted
	one, _ = third["corners"].([]any)[0].(map[string]any)
	r.EqualValues(92, one["ref_apex_kmh"], "still the best lap, not the last")

	// A lap that was not clean is never the reference, however quick.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapCompleted{
		At: at, Stint: stint, Lap: 3, LapMs: 90000, LapKind: wire.KindInvalid, Corners: slow,
	}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 4, Corners: slow}))
	fourth := <-posted
	one, _ = fourth["corners"].([]any)[0].(map[string]any)
	r.EqualValues(92, one["ref_apex_kmh"])

	// A new stint starts again from nothing.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{At: at, Stint: stint, Lap: 1, Corners: slow}))
	again := <-posted
	_, named = again["reference"]
	r.False(named)
	r.NotNil(h)
}

// Corners are matched by where they sit on the lap, not by their number: a lap
// where the detector found one corner fewer must not compare Turn 4 against
// Turn 5 and send the driver braking for the wrong bend.
func TestCornersAreMatchedByPlaceNotNumber(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ref := []clientplugin.Corner{
		{Turn: 1, ApexPct: 100, ApexKmh: 90},
		{Turn: 2, ApexPct: 300, ApexKmh: 70},
		{Turn: 3, ApexPct: 700, ApexKmh: 110},
	}
	// This lap missed the corner at 300: its Turn 2 is the reference's Turn 3.
	lap := []clientplugin.Corner{{Turn: 1, ApexPct: 98, ApexKmh: 88}, {Turn: 2, ApexPct: 706, ApexKmh: 100}}
	got := engineer.Compare(lap, ref)
	r.Len(got, 2)
	r.Equal(90, got[0].RefApexKmh)
	r.Equal(110, got[1].RefApexKmh, "matched by place: the corner at 700")
	r.Equal(10, got[1].DeficitKmh)

	// A corner too far from any reference corner is left uncompared, and a
	// reference corner is used once however many corners sit near it.
	far := engineer.Compare([]clientplugin.Corner{{Turn: 1, ApexPct: 500, ApexKmh: 80}}, ref)
	r.Zero(far[0].RefApexKmh)
	twice := engineer.Compare([]clientplugin.Corner{
		{Turn: 1, ApexPct: 100, ApexKmh: 80}, {Turn: 2, ApexPct: 102, ApexKmh: 80},
	}, ref)
	r.Equal(90, twice[0].RefApexKmh)
	r.Zero(twice[1].RefApexKmh, "the reference corner was already spoken for")

	// Across the start line the nearest corner is still the nearest.
	wrapped := engineer.Compare([]clientplugin.Corner{{Turn: 1, ApexPct: 995, ApexKmh: 80}},
		[]clientplugin.Corner{{Turn: 1, ApexPct: 5, ApexKmh: 95}})
	r.Equal(95, wrapped[0].RefApexKmh)
	r.Equal(15, wrapped[0].DeficitKmh)

	// With no reference at all the measurements go on their own.
	alone := engineer.Compare(lap, nil)
	r.Len(alone, 2)
	r.Zero(alone[0].RefApexKmh)
	r.Zero(alone[0].DeficitKmh)
	r.Equal(88, alone[0].ApexKmh)
}

// The Turn 1 report sent a lap ahead says so, so that engineer writes its line
// without buying audio for a line the full lap usually replaces.
func TestTheEarlyReportSaysItIsEarly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	stint := clientplugin.Stint{ID: "s-early", Session: wire.SessionPractice, TrackLengthM: 3650, SetupOpen: true}

	posted := make(chan map[string]any, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, req *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(req.Body).Decode(&rep)
		posted <- rep
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stint_id": rep["stint_id"], "lap": rep["lap"], "cues": []any{}})
	})
	c, _, _ := newCompanionWithClock(t, mux)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))

	// Turn 1 passed: the early report, marked early and with the car left out.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.CornerPassed{
		At: at, Stint: stint, Lap: 2, Corner: clientplugin.Corner{Turn: 1, ApexPct: 100, ApexKmh: 80},
	}))
	early := <-posted
	r.Equal(true, early["early"])
	_, setup := early["setup_open"]
	r.False(setup, "the early report is about one corner, not the car")

	// The lap's own report at the last corner is not early.
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{
		At: at, Stint: stint, Lap: 2, Corners: []clientplugin.Corner{{Turn: 1, ApexPct: 100, ApexKmh: 80}},
	}))
	full := <-posted
	_, marked := full["early"]
	r.False(marked)
	r.Equal(true, full["setup_open"])
}

// A client whose sound is off tells engineer so, and engineer writes the lines
// without buying audio for them. A client with no companion that plays at all
// says the same thing.
func TestASilentClientSaysSo(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	stint := clientplugin.Stint{ID: "s-silent", Session: wire.SessionPractice, TrackLengthM: 3650}

	posted := make(chan map[string]any, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /laps", func(w http.ResponseWriter, req *http.Request) {
		var rep map[string]any
		_ = json.NewDecoder(req.Body).Decode(&rep)
		posted <- rep
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"stint_id": rep["stint_id"], "lap": rep["lap"], "cues": []any{}})
	})

	// A host that hears: nothing is said about silence.
	c, _, _ := newCompanionWithClock(t, mux)
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.NoError(clientplugintest.Deliver(ctx, c, &clientplugin.LapLastCorner{
		At: at, Stint: stint, Lap: 1, Corners: []clientplugin.Corner{{Turn: 1, ApexPct: 100, ApexKmh: 80}},
	}))
	loud := <-posted
	_, said := loud["silent"]
	r.False(said, "a client that plays audio asks for it by saying nothing")

	// A host that hears nothing: every lap says so.
	quiet := engineer.New()
	h := clientplugintest.NewHost(t, mux).WithSilence()
	r.NoError(quiet.Start(ctx, h))
	t.Cleanup(func() { _ = quiet.Stop() })
	r.NoError(clientplugintest.Deliver(ctx, quiet, &clientplugin.StintStarted{At: at, Stint: stint}))
	r.NoError(clientplugintest.Deliver(ctx, quiet, &clientplugin.LapLastCorner{
		At: at, Stint: stint, Lap: 1, Corners: []clientplugin.Corner{{Turn: 1, ApexPct: 100, ApexKmh: 80}},
	}))
	off := <-posted
	r.Equal(true, off["silent"])
}
