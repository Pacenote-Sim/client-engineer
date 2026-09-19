package engineer

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// page renders what the companion knows into the app's window: the latest
// lines by corner, what the lap said about the car, the radio, the debrief.
// Plain HTML, no scripts; the window styles it.
func (c *Companion) page() {
	c.mu.Lock()
	h := c.host
	out := c.render()
	c.mu.Unlock()
	if h != nil {
		h.Page(out)
	}
}

func (c *Companion) render() string {
	var b strings.Builder
	if c.debrief != nil {
		d := c.debrief
		b.WriteString("<h2>Debrief")
		if d.Track != "" {
			b.WriteString(" · " + html.EscapeString(d.Track))
		}
		b.WriteString("</h2>\n")
		if d.Summary != "" {
			b.WriteString("<p>" + html.EscapeString(d.Summary) + "</p>\n")
		}
		if len(d.Findings) > 0 {
			b.WriteString("<ul>\n")
			for _, f := range d.Findings {
				fmt.Fprintf(&b, "  <li><b>%s</b> · %s", html.EscapeString(f.Area), html.EscapeString(f.Observation))
				if f.Drill != "" {
					b.WriteString(" <i>" + html.EscapeString(f.Drill) + "</i>")
				}
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul>\n")
		}
		if len(d.Setup) > 0 {
			b.WriteString("<h3>Setup</h3>\n<ul>\n")
			for _, s := range d.Setup {
				fmt.Fprintf(&b, "  <li><b>%s</b>", html.EscapeString(s.Setting))
				if s.From != "" {
					b.WriteString(" " + html.EscapeString(s.From) + " →")
				}
				fmt.Fprintf(&b, " %s · %s</li>\n", html.EscapeString(s.To), html.EscapeString(s.Because))
			}
			b.WriteString("</ul>\n")
		}
	}
	if lap, ans, ok := c.latest(); ok {
		fmt.Fprintf(&b, "<h2>Lines from lap %d</h2>\n", lap)
		if len(ans.Cues) == 0 {
			b.WriteString("<p>Nothing to coach on that lap.</p>\n")
		} else {
			b.WriteString("<ul>\n")
			for _, l := range ans.Cues {
				if l.Turn == 0 {
					continue
				}
				fmt.Fprintf(&b, "  <li><b>T%d</b> %s</li>\n", l.Turn, html.EscapeString(l.Line))
			}
			b.WriteString("</ul>\n")
		}
	}
	if c.radio != "" {
		b.WriteString("<h3>Radio</h3>\n<p>" + html.EscapeString(c.radio) + "</p>\n")
	}
	if len(c.notes) > 0 {
		b.WriteString("<h3>About the car</h3>\n<ul>\n")
		for _, n := range c.notes {
			b.WriteString("  <li>" + html.EscapeString(n) + "</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	if b.Len() == 0 {
		return "<p>No lines yet. The first lap of a stint is measured; the lines start from the second.</p>\n"
	}
	return b.String()
}

// latest is the most recent lap with an answer.
func (c *Companion) latest() (int, answer, bool) {
	if len(c.cues) == 0 {
		return 0, answer{}, false
	}
	laps := make([]int, 0, len(c.cues))
	for l := range c.cues {
		laps = append(laps, l)
	}
	sort.Ints(laps)
	last := laps[len(laps)-1]
	return last, c.cues[last], true
}
