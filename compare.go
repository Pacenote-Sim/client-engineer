package engineer

import (
	"github.com/pacenote-sim/clientplugin"
	"github.com/pacenote-sim/protocol/wire"
)

// The comparison. A coach with one lap can only say what the driver did; a
// coach with two can say what it cost and where it went. The second lap is the
// driver's own best so far this stint, which the client has in hand: every lap
// arrives complete, with its time and its corners, so the reference is kept as
// the stint goes on and no reference lap has to be fetched from anywhere.
//
// Corners are matched by where they sit on the lap and not by their number,
// because a lap where the detector found one corner fewer would otherwise
// compare Turn 4 against Turn 5 and tell the driver to brake for the wrong one.

// ReferenceName is what the coach calls the lap being compared against. It is
// read aloud as part of the lap's facts, so it is a phrase and not a key.
const ReferenceName = "your best lap so far this stint"

// MatchApexPct is how far, in thousandths of the lap, a reference corner's apex
// may sit from this one's and still be the same corner.
const MatchApexPct = 40

// reportCorner is a corner as engineer's POST /laps takes it: what was measured
// this lap, and the same corner on the reference lap beside it.
type reportCorner struct {
	Turn    int `json:"turn"`
	ApexPct int `json:"apex_pct"`

	ApexKmh    int `json:"apex_kmh,omitempty"`
	RefApexKmh int `json:"ref_apex_kmh,omitempty"`
	// DeficitKmh is how much apex speed this corner lost against the
	// reference, positive, and zero for a corner that gained.
	DeficitKmh int `json:"deficit_kmh,omitempty"`

	MinKmh     int `json:"min_kmh,omitempty"`
	RefMinKmh  int `json:"ref_min_kmh,omitempty"`
	ExitKmh    int `json:"exit_kmh,omitempty"`
	RefExitKmh int `json:"ref_exit_kmh,omitempty"`

	BrakeAtPct     int `json:"brake_at_pct,omitempty"`
	RefBrakeAtPct  int `json:"ref_brake_at_pct,omitempty"`
	PeakBrakePct   int `json:"peak_brake_pct,omitempty"`
	TurnInBrakePct int `json:"turn_in_brake_pct,omitempty"`
	BrakeAtApex    int `json:"brake_at_apex,omitempty"`

	ThrottleLag    int `json:"throttle_lag,omitempty"`
	RefThrottleLag int `json:"ref_throttle_lag,omitempty"`
	GearAtApex     int `json:"gear_at_apex,omitempty"`
	RefGearAtApex  int `json:"ref_gear_at_apex,omitempty"`

	Pattern wire.CornerPattern `json:"pattern,omitempty"`
}

// compare renders this lap's corners for the report, each with the same corner
// from the reference lap beside it. With no reference lap the measurements go
// on their own and the coach speaks in absolutes, which is what the first laps
// of a stint get.
func compare(corners, ref []clientplugin.Corner) []reportCorner {
	out := make([]reportCorner, 0, len(corners))
	used := make([]bool, len(ref))
	for _, c := range corners {
		rc := reportCorner{
			Turn: c.Turn, ApexPct: c.ApexPct, ApexKmh: c.ApexKmh, MinKmh: c.MinKmh, ExitKmh: c.ExitKmh,
			BrakeAtPct: c.BrakeAtPct, PeakBrakePct: c.PeakBrakePct, TurnInBrakePct: c.TurnInBrakePct,
			BrakeAtApex: c.BrakeAtApex, ThrottleLag: c.ThrottleLag, GearAtApex: c.GearAtApex, Pattern: c.Pattern,
		}
		if i, ok := nearest(ref, used, c.ApexPct); ok {
			used[i] = true
			r := ref[i]
			rc.RefApexKmh, rc.RefMinKmh, rc.RefExitKmh = r.ApexKmh, r.MinKmh, r.ExitKmh
			rc.RefBrakeAtPct, rc.RefThrottleLag, rc.RefGearAtApex = r.BrakeAtPct, r.ThrottleLag, r.GearAtApex
			if d := r.ApexKmh - c.ApexKmh; d > 0 {
				rc.DeficitKmh = d
			}
		}
		out = append(out, rc)
	}
	return out
}

// nearest is the unused reference corner closest to an apex, and whether one
// is close enough to be the same corner.
func nearest(ref []clientplugin.Corner, used []bool, apexPct int) (int, bool) {
	best, found := 0, false
	bestD := MatchApexPct + 1
	for i, r := range ref {
		if used[i] {
			continue
		}
		d := abs(r.ApexPct - apexPct)
		if d > 500 {
			d = 1000 - d
		}
		if d < bestD {
			best, bestD, found = i, d, true
		}
	}
	return best, found
}
