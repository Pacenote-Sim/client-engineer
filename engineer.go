// Package engineer is the client half of the engineer server plugin: the
// companion that posts each lap's corners to engineer after the last corner,
// plays engineer's line for each corner as the corner approaches, fetches the
// radio line after a race lap, and shows the debrief when the stint is over.
//
// It measures nothing and compares nothing. The app measures; engineer
// compares; this carries one to the other and plays what comes back.
package engineer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pacenote-sim/clientplugin"
	"github.com/pacenote-sim/protocol/wire"
)

func init() { clientplugin.RegisterCompanion(New()) }

// Name is the server plugin this is the client half of.
const Name = "engineer"

// The timing.
const (
	// DebriefDelay is how long after the stint ends the first look for the
	// debrief waits: engineer writes it a few seconds after the summary.
	DebriefDelay = 5 * time.Second
	// DebriefRetry is the pause between looks, and DebriefTries how many.
	DebriefRetry = 5 * time.Second
	DebriefTries = 24
	// MatchPct is how far, in thousandths of the lap, a line's apex may sit
	// from the approaching corner's and still be its line.
	MatchPct = 40
	// MaxAnswerBytes bounds an answer: lines with audio for forty corners.
	MaxAnswerBytes = 32 << 20
	// WordsPerSecond is how fast a line is spoken, for judging whether one
	// fits before the corner; LineLeadIn is what a line costs beyond its words.
	// The pace is the one the voice vendor's own metering implies — 750
	// characters to the minute of audio, about 132 words — and not the faster
	// one a reader imagines.
	WordsPerSecond = 2.2
	LineLeadIn     = 600 * time.Millisecond
	// Margin is how long before the braking point a line must have ended.
	Margin = time.Second
	// HoldSlack is how far before the last possible moment a held line is
	// started, so that a corner arrived at faster than the lap before does not
	// cost the line.
	HoldSlack = 2 * time.Second
)

// Companion is the companion.
type Companion struct {
	// Sleep waits; a test replaces it.
	Sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	host   clientplugin.Host
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// waiting are lines whose corner is still too far off to speak: the cue
	// was raised with room for the longest line there could be, and a shorter
	// one is held until it has to start.
	waiting []held

	stint   clientplugin.Stint
	cues    map[int]answer // the full answer, by the lap that was posted
	early   map[int]answer // the early answer for Turn 1 alone, by lap
	played  map[string]bool
	notes   []string
	debrief *debrief
	radio   string

	// Where the car is, and the corner it approached without a line: when the
	// answer arrives late, the line is played at once if the apex is still ahead.
	lap     int
	pct     int
	speed   float64 // km/h
	pending *clientplugin.CornerApproaching

	// best is the corners of the driver's own best clean lap this stint and
	// bestMs its time: the lap every later lap is compared against, so that
	// the coach can say what a corner cost and not only what happened in it.
	best   []clientplugin.Corner
	bestMs int

	// The schedule: the previous lap's corners, for braking points; when the
	// speaker is free again; and how many laps each corner's line has been
	// skipped for, so that a corner squeezed out by its neighbour gets its
	// turn next lap.
	corners  []clientplugin.Corner
	busy     time.Time
	deferred map[int]int
	Now      func() time.Time
}

// New is a companion with nothing heard yet.
func New() *Companion {
	return &Companion{Sleep: sleep, cues: map[int]answer{}, early: map[int]answer{}, played: map[string]bool{}, deferred: map[int]int{}, Now: time.Now}
}

// Name implements [clientplugin.Companion].
func (*Companion) Name() string { return Name }

// Wants implements [clientplugin.Companion].
func (*Companion) Wants() []clientplugin.EventKind {
	return []clientplugin.EventKind{
		clientplugin.KindStintStarted, clientplugin.KindSampled, clientplugin.KindCornerPassed, clientplugin.KindLapLastCorner,
		clientplugin.KindCornerApproaching, clientplugin.KindLapCompleted, clientplugin.KindStintFinished,
	}
}

// Start implements [clientplugin.Companion].
func (c *Companion) Start(ctx context.Context, h clientplugin.Host) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.host = h
	c.ctx, c.cancel = context.WithCancel(ctx)
	h.Status("waiting for a lap")
	return nil
}

// Stop implements [clientplugin.Companion]: everything in flight is ended and waited for.
func (c *Companion) Stop() error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
	return nil
}

// Notify implements [clientplugin.Companion]. Anything that waits on the
// network runs in its own goroutine under the Start context.
func (c *Companion) Notify(_ context.Context, e clientplugin.Event) error {
	switch e := e.(type) {
	case *clientplugin.StintStarted:
		c.reset(e.Stint)
	case *clientplugin.Sampled:
		c.mu.Lock()
		c.lap, c.pct, c.speed = e.Lap, int(e.Sample.LapDistPct*1000), e.Sample.SpeedKmh
		due := c.due()
		c.mu.Unlock()
		for i := range due {
			if err := c.speak(&due[i].at, due[i].line); err != nil {
				return err
			}
		}
	case *clientplugin.CornerPassed:
		// Turn 1 alone, the moment it is behind the car: its line for the
		// next lap is then ready with a whole lap to spare, whatever the
		// straight's length. The full lap follows at the last corner.
		if e.Corner.Turn == 1 {
			early := &clientplugin.LapLastCorner{At: e.At, Stint: e.Stint, Lap: e.Lap, Corners: []clientplugin.Corner{e.Corner}}
			c.spawn(func(ctx context.Context) { c.post(ctx, early, true) })
		}
	case *clientplugin.LapLastCorner:
		c.mu.Lock()
		c.corners = append([]clientplugin.Corner(nil), e.Corners...)
		c.mu.Unlock()
		c.spawn(func(ctx context.Context) { c.post(ctx, e, false) })
	case *clientplugin.CornerApproaching:
		return c.approach(e)
	case *clientplugin.LapCompleted:
		c.keepBest(e)
		if e.Stint.Session == wire.SessionRace {
			c.spawn(func(ctx context.Context) { c.fetchRadio(ctx, e) })
		}
	case *clientplugin.StintFinished:
		c.spawn(func(ctx context.Context) { c.fetchDebrief(ctx, e.Stint) })
	}
	return nil
}

func (c *Companion) spawn(f func(context.Context)) {
	c.mu.Lock()
	ctx := c.ctx
	c.mu.Unlock()
	if ctx == nil {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		f(ctx)
	}()
}

func (c *Companion) reset(s clientplugin.Stint) {
	c.mu.Lock()
	c.stint = s
	c.cues = map[int]answer{}
	c.early = map[int]answer{}
	c.played = map[string]bool{}
	c.notes, c.debrief, c.radio = nil, nil, ""
	c.lap, c.pct, c.speed, c.pending, c.waiting = 0, 0, 0, nil, nil
	c.corners, c.busy, c.deferred = nil, time.Time{}, map[int]int{}
	c.best, c.bestMs = nil, 0
	c.mu.Unlock()
	c.status("waiting for the first lap")
	c.page()
}

// audible reports that this client would play a line it was sent.
func (c *Companion) audible() bool {
	c.mu.Lock()
	h := c.host
	c.mu.Unlock()
	return h != nil && h.Audible()
}

// keepBest remembers the corners of the best clean lap of the stint. It is the
// lap the driver is compared against: their own, in this car, on this circuit,
// today, which is the only reference this client can be sure means anything.
func (c *Companion) keepBest(e *clientplugin.LapCompleted) {
	if e.LapKind != wire.KindClean || e.LapMs <= 0 || len(e.Corners) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bestMs == 0 || e.LapMs < c.bestMs {
		c.best, c.bestMs = append([]clientplugin.Corner(nil), e.Corners...), e.LapMs
	}
}

// report is what engineer's POST /laps takes: this lap's corners, as measured,
// nothing compared. Engineer compares.
type report struct {
	StintID      string           `json:"stint_id"`
	Lap          int              `json:"lap"`
	Session      wire.SessionType `json:"session"`
	TrackLengthM int              `json:"track_length_m,omitempty"`
	// Early marks the Turn 1 report sent a lap ahead. Engineer writes its line
	// but does not buy audio for it: the full lap writes Turn 1 again and that
	// is the line usually heard.
	Early bool `json:"early,omitempty"`
	// Silent says nothing on this client will be heard — no companion plays,
	// or the driver has turned the sound off — so the lines should be written
	// and not spoken. Audio is bought by the character; buying it for a driver
	// who is reading is the one waste nobody notices.
	Silent    bool `json:"silent,omitempty"`
	SetupOpen bool `json:"setup_open,omitempty"`
	// Reference names the lap the corners are compared against, in words the
	// coach reads aloud, and is empty before there is one.
	Reference string         `json:"reference,omitempty"`
	Corners   []reportCorner `json:"corners"`
}

// cue is one of engineer's lines: where to say it, the words, and the audio
// when the voice plugin is installed.
type cue struct {
	Turn      int    `json:"turn"`
	ApexPct   int    `json:"apex_pct"`
	Line      string `json:"line"`
	Audio     []byte `json:"audio,omitempty"`
	AudioType string `json:"audio_type,omitempty"`
}

// answer is engineer's answer to a lap, and to GET /cues.
type answer struct {
	Lap        int      `json:"lap"`
	Mode       string   `json:"mode"`
	Reference  string   `json:"reference"`
	Cues       []cue    `json:"cues"`
	SetupNotes []string `json:"setup_notes"`
}

// post sends a report and keeps the lines for the next lap: the full lap at
// its last corner, or Turn 1 alone early. The early report says nothing about
// the car's setup, so that engineer's notes about the car come once, from the
// full one.
func (c *Companion) post(ctx context.Context, e *clientplugin.LapLastCorner, early bool) {
	c.mu.Lock()
	ref := c.best
	c.mu.Unlock()
	rep := report{
		StintID: e.Stint.ID, Lap: e.Lap, Session: e.Stint.Session, TrackLengthM: e.Stint.TrackLengthM,
		Early: early, Silent: !c.audible(), SetupOpen: e.Stint.SetupOpen && !early,
		Corners: compare(e.Corners, ref),
	}
	if len(ref) > 0 {
		rep.Reference = ReferenceName
	}
	body, _ := json.Marshal(rep)
	var ans answer
	status, err := c.call(ctx, http.MethodPost, "/laps", bytes.NewReader(body), &ans)
	switch {
	case err != nil && status == http.StatusOK:
		c.status("engineer's answer could not be read")
		c.log().Warn("engineer answered something unreadable", "lap", e.Lap, "reason", err.Error())
		return
	case err != nil:
		c.status("engineer could not be reached")
		c.log().Warn("the lap could not be posted", "lap", e.Lap, "reason", err.Error())
		return
	case status == http.StatusServiceUnavailable:
		c.status("engineer has no key; nothing is written")
		return
	case status == http.StatusBadGateway:
		c.status(fmt.Sprintf("the coach did not answer for lap %d", e.Lap))
		return
	case status != http.StatusOK:
		c.status(fmt.Sprintf("engineer refused lap %d: %d", e.Lap, status))
		c.log().Warn("engineer refused the lap", "lap", e.Lap, "status", status)
		return
	}
	c.mu.Lock()
	if early {
		c.early[e.Lap] = ans
	} else {
		c.cues[e.Lap] = ans
		c.notes = append(c.notes, ans.SetupNotes...)
	}
	late := c.pending
	if late != nil && (late.Lap != e.Lap+1 || !ahead(c.pct, late.ApexPct)) {
		late = nil // the corner is behind the car, or another lap's
	}
	c.pending = nil
	c.mu.Unlock()
	if early {
		c.status(fmt.Sprintf("Turn 1 ready for lap %d", e.Lap+1))
	} else {
		c.status(fmt.Sprintf("%d lines ready for lap %d", len(ans.Cues), e.Lap+1))
		c.page()
	}
	if late != nil {
		// The answer came after the corner came into range but before its
		// apex: the line is late, not lost.
		if err := c.approach(late); err != nil {
			c.log().Warn("a late line could not be played", "turn", late.Turn, "reason", err.Error())
		}
	}
}

// ahead reports that the apex is still in front of the car, going forward
// round the lap and allowing for the line between them.
func ahead(pct, apexPct int) bool {
	d := apexPct - pct
	if d < 0 {
		d += 1000
	}
	return d > 0 && d < 500
}

// held is a line waiting for the moment it has to start.
type held struct {
	at   clientplugin.CornerApproaching
	line cue
}

// due is the held lines whose moment has come, and drops the ones whose corner
// the car has already reached. Called with the lock held.
func (c *Companion) due() []held {
	if len(c.waiting) == 0 {
		return nil
	}
	var out, keep []held
	for i := range c.waiting {
		w := &c.waiting[i]
		switch {
		case w.at.Lap != c.lap || !ahead(c.pct, w.at.ApexPct):
			// The corner is behind the car, or the lap moved on: the line was
			// for a corner that is no longer ahead and is dropped.
		case c.earlyBy(&w.at, w.line) > 0:
			keep = append(keep, *w)
		default:
			out = append(out, *w)
		}
	}
	c.waiting = keep
	return out
}

// earlyBy is how long a line can still wait before it has to start, so that it
// ends a margin before the braking point. Zero when there is no time to judge
// it by. Called with the lock held.
func (c *Companion) earlyBy(e *clientplugin.CornerApproaching, line cue) time.Duration {
	until, known := c.timeTo(e.Turn, e.ApexPct)
	if !known {
		return 0
	}
	return until - duration(line.Line) - Margin - HoldSlack
}

// approach takes the corner ahead: it finds the line for it, from the report of
// the lap before or, when that answer is still not back, from the lap before
// that. A line whose corner is still far off waits for the moment it has to
// start; one whose moment has come is spoken.
func (c *Companion) approach(e *clientplugin.CornerApproaching) error {
	c.mu.Lock()
	line, ok := c.lineFor(e.Lap-1, e)
	if !ok {
		// No answer for the previous lap has this corner yet. Remember it:
		// if one arrives before its apex, the line is played then.
		if _, posted := c.cues[e.Lap-1]; !posted {
			cp := *e
			c.pending = &cp
		}
		line, ok = c.lineFor(e.Lap-2, e)
	}
	if !ok {
		c.mu.Unlock()
		return nil
	}
	// The cue was raised with room for the longest line a coach may write. A
	// shorter one waits: heard at the last moment that still ends before the
	// braking point, it is the freshest thing in the driver's head there.
	if c.earlyBy(e, line) > 0 {
		c.waiting = append(c.waiting, held{at: *e, line: line})
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.speak(e, line)
}

// speak plays the line for the corner now, unless a rule holds it back: the
// speaker is busy, the words no longer fit, or the next corner has waited
// longer and would be squeezed out. A line plays once per lap.
func (c *Companion) speak(e *clientplugin.CornerApproaching, line cue) error {
	c.mu.Lock()
	// The key is the line's own turn, not the corner's: a line the coach
	// wrote for two corners too close for a line each is filed under the
	// first and matches the second by distance, and is heard once.
	key := strconv.Itoa(e.Lap) + ":" + strconv.Itoa(line.Turn)
	if c.played[key] {
		c.mu.Unlock()
		return nil
	}
	if reason := c.skip(e, line); reason != "" {
		c.deferred[e.Turn]++
		h := c.host
		c.mu.Unlock()
		if h != nil {
			h.Log().Info("a line was held back", "turn", e.Turn, "reason", reason)
		}
		return nil
	}
	c.played[key] = true
	c.deferred[e.Turn] = 0
	c.busy = c.Now().Add(duration(line.Line))
	h, ctx := c.host, c.ctx
	c.mu.Unlock()
	if h == nil {
		return nil
	}
	// What was spoken, and when: the one record of what the driver actually
	// heard, which is what a complaint about the coach is answered from.
	h.Log().Info("a line was spoken", "turn", line.Turn, "lap", e.Lap,
		"words", len(strings.Fields(line.Line)), "audio", len(line.Audio) > 0, "line", line.Line)
	if len(line.Audio) > 0 {
		if err := h.Play(ctx, line.Audio, line.AudioType); err != nil {
			return fmt.Errorf("engineer: playing the line for turn %d: %w", e.Turn, err)
		}
		return nil
	}
	if err := h.Say(ctx, line.Line); err != nil {
		return fmt.Errorf("engineer: saying the line for turn %d: %w", e.Turn, err)
	}
	return nil
}

// skip says why the line for this corner is not played now, or "" to play it.
// Called with the lock held.
func (c *Companion) skip(e *clientplugin.CornerApproaching, line cue) string {
	now := c.Now()
	if now.Before(c.busy) {
		return "another line is still being spoken"
	}
	need := duration(line.Line) + Margin
	if until, known := c.timeTo(e.Turn, e.ApexPct); known && until < need {
		return fmt.Sprintf("it needs %.1f s and the braking point is %.1f s away", need.Seconds(), until.Seconds())
	}
	// Would this line squeeze out the next corner's, which has waited longer?
	// The two corners' braking points are the gap the two lines have to share.
	if next, ok := c.nextCorner(e.Turn); ok && c.deferred[next.Turn] > c.deferred[e.Turn] {
		mine, k1 := c.timeTo(e.Turn, e.ApexPct)
		theirs, k2 := c.timeTo(next.Turn, next.ApexPct)
		if _, has := c.lineFor(e.Lap-1, &clientplugin.CornerApproaching{Turn: next.Turn, ApexPct: next.ApexPct}); has && k1 && k2 {
			if theirs-mine < duration(line.Line)+Margin {
				return fmt.Sprintf("turn %d's line has waited longer and would be squeezed out", next.Turn)
			}
		}
	}
	return ""
}

// timeTo is how long until the driver has to brake for the corner: the
// distance to its braking point from the previous lap, at the current speed.
// Unknown without the track length or a speed.
func (c *Companion) timeTo(turn, apexPct int) (time.Duration, bool) {
	if c.stint.TrackLengthM == 0 || c.speed <= 1 {
		return 0, false
	}
	point := apexPct
	for _, k := range c.corners {
		if k.Turn == turn && k.BrakeAtPct > 0 {
			point = k.BrakeAtPct
		}
	}
	d := point - c.pct
	if d < 0 {
		d += 1000
	}
	metres := float64(d) * float64(c.stint.TrackLengthM) / 1000
	return time.Duration(metres / (c.speed / 3.6) * float64(time.Second)), true
}

// nextCorner is the corner after this one on the previous lap.
func (c *Companion) nextCorner(turn int) (clientplugin.Corner, bool) {
	for i, k := range c.corners {
		if k.Turn == turn && i+1 < len(c.corners) {
			return c.corners[i+1], true
		}
	}
	return clientplugin.Corner{}, false
}

// duration is how long a line takes to say.
func duration(line string) time.Duration {
	words := len(strings.Fields(line))
	return LineLeadIn + time.Duration(float64(words)/WordsPerSecond*float64(time.Second))
}

// lineFor finds the line for the approaching corner in the answers to lap:
// the full one first, the early Turn 1 one otherwise. A line matches by the
// same turn number, or an apex within MatchPct.
func (c *Companion) lineFor(lap int, e *clientplugin.CornerApproaching) (cue, bool) {
	if ans, ok := c.cues[lap]; ok {
		if l, found := match(ans, e); found {
			return l, true
		}
	}
	if ans, ok := c.early[lap]; ok {
		return match(ans, e)
	}
	return cue{}, false
}

func match(ans answer, e *clientplugin.CornerApproaching) (cue, bool) {
	best, found, bestD := cue{}, false, MatchPct+1
	for _, l := range ans.Cues {
		if l.Turn == 0 || l.Line == "" {
			continue // the radio line is not a corner's
		}
		d := abs(l.ApexPct - e.ApexPct)
		if d > 500 {
			d = 1000 - d
		}
		if l.Turn == e.Turn && l.ApexPct == 0 {
			d = 0
		}
		if d < bestD {
			best, found, bestD = l, true, d
		}
	}
	return best, found
}

// fetchRadio asks for the radio line after a race lap and plays it.
func (c *Companion) fetchRadio(ctx context.Context, e *clientplugin.LapCompleted) {
	q := url.Values{"stint": {e.Stint.ID}, "lap": {strconv.Itoa(e.Lap)}}
	var ans answer
	status, err := c.call(ctx, http.MethodGet, "/cues?"+q.Encode(), nil, &ans)
	if err != nil || status != http.StatusOK {
		return
	}
	for _, l := range ans.Cues {
		if l.Turn != 0 || l.Line == "" {
			continue
		}
		c.mu.Lock()
		c.radio = l.Line
		h := c.host
		c.mu.Unlock()
		c.page()
		var perr error
		if len(l.Audio) > 0 {
			perr = h.Play(ctx, l.Audio, l.AudioType)
		} else {
			perr = h.Say(ctx, l.Line)
		}
		if perr != nil {
			c.log().Warn("the radio line could not be played", "reason", perr.Error())
		}
	}
}

type finding struct {
	Area        string `json:"area"`
	Observation string `json:"observation"`
	Drill       string `json:"drill"`
}

type setupChange struct {
	Setting string `json:"setting"`
	From    string `json:"from"`
	To      string `json:"to"`
	Because string `json:"because"`
}

type debrief struct {
	Track    string        `json:"track"`
	Car      string        `json:"car"`
	Summary  string        `json:"summary"`
	Findings []finding     `json:"findings"`
	Setup    []setupChange `json:"setup_changes"`
}

// fetchDebrief waits for engineer to write the debrief, then shows it.
func (c *Companion) fetchDebrief(ctx context.Context, s clientplugin.Stint) {
	c.status("the stint is over; waiting for the debrief")
	if err := c.Sleep(ctx, DebriefDelay); err != nil {
		return
	}
	for range DebriefTries {
		var d debrief
		status, err := c.call(ctx, http.MethodGet, "/debrief?stint="+url.QueryEscape(s.ID), nil, &d)
		switch {
		case err != nil:
			c.status("engineer could not be reached for the debrief")
			return
		case status == http.StatusOK:
			c.mu.Lock()
			c.debrief = &d
			c.mu.Unlock()
			c.status("debrief ready")
			c.page()
			return
		case status != http.StatusNotFound:
			c.status(fmt.Sprintf("no debrief: engineer answered %d", status))
			return
		}
		if err := c.Sleep(ctx, DebriefRetry); err != nil {
			return
		}
	}
	c.status("no debrief was written for this stint")
}

// call sends one request to engineer and decodes a JSON answer into out when
// the status is 200. It returns the status.
func (c *Companion) call(ctx context.Context, method, path string, body io.Reader, out any) (int, error) {
	c.mu.Lock()
	h := c.host
	c.mu.Unlock()
	if h == nil {
		return 0, errors.New("engineer: not started")
	}
	var header http.Header
	if body != nil {
		header = http.Header{"Content-Type": {"application/json"}}
	}
	res, err := h.Do(ctx, method, path, body, header)
	if err != nil {
		return 0, err //nolint:wrapcheck // the host's words name the plugin.
	}
	defer res.Body.Close() //nolint:errcheck // a read answer.
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxAnswerBytes))
	if err != nil {
		return res.StatusCode, fmt.Errorf("engineer: reading the answer: %w", err)
	}
	if res.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("engineer: the answer was not the JSON it should be: %w", err)
		}
	}
	return res.StatusCode, nil
}

func (c *Companion) status(text string) {
	c.mu.Lock()
	h := c.host
	c.mu.Unlock()
	if h != nil {
		h.Status(text)
	}
}

func (c *Companion) log() logger {
	c.mu.Lock()
	h := c.host
	c.mu.Unlock()
	if h == nil {
		return discard{}
	}
	return h.Log()
}

type logger interface {
	Warn(msg string, args ...any)
}

type discard struct{}

func (discard) Warn(string, ...any) {}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the context's own reason is the reason.
	case <-t.C:
		return nil
	}
}
