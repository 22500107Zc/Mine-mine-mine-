package saas

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"
)

// All charts are rendered on the server as inline SVG: no chart library, no
// client data, and every label is escaped.

// sparkline draws a small trend line; NaN points are gaps
func sparkline(series []float64, status string) template.HTML {
	const w, h, pad = 120.0, 30.0, 3.0
	lo, hi := math.Inf(1), math.Inf(-1)
	n := 0
	for _, v := range series {
		if math.IsNaN(v) {
			continue
		}
		n++
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if n < 2 {
		return template.HTML(`<svg class="spark empty" viewBox="0 0 120 30" aria-hidden="true"><line x1="3" y1="27" x2="117" y2="27"/></svg>`)
	}
	if hi == lo {
		hi, lo = hi+1, lo-1
	}
	step := (w - 2*pad) / float64(max(1, len(series)-1))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="spark %s" viewBox="0 0 120 30" preserveAspectRatio="none" aria-hidden="true">`, html.EscapeString(status))
	var pts []string
	flush := func() {
		if len(pts) > 1 {
			fmt.Fprintf(&b, `<polyline points="%s"/>`, strings.Join(pts, " "))
		} else if len(pts) == 1 {
			var x, y float64
			fmt.Sscanf(pts[0], "%f,%f", &x, &y)
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="1.5"/>`, x, y)
		}
		pts = pts[:0]
	}
	lastX, lastY := 0.0, 0.0
	for i, v := range series {
		if math.IsNaN(v) {
			flush()
			continue
		}
		x := pad + float64(i)*step
		y := h - pad - (v-lo)/(hi-lo)*(h-2*pad)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
		lastX, lastY = x, y
	}
	flush()
	fmt.Fprintf(&b, `<circle class="last" cx="%.1f" cy="%.1f" r="2"/></svg>`, lastX, lastY)
	return template.HTML(b.String())
}

func bandClass(band string) string {
	switch band {
	case "Critical":
		return "bad"
	case "High":
		return "warn"
	case "Elevated":
		return "elev"
	case "Normal":
		return "ok"
	}
	return "none"
}

// processMap draws a workflow as stage nodes with forward moves above the
// line of stages and rework loops below it; nodes and edges are links
func processMap(ps ProcessStages, scope Scope) template.HTML {
	if len(ps.Rows) == 0 {
		return ""
	}
	const nodeW, nodeH, gap, top = 140.0, 108.0, 74.0, 150.0
	n := len(ps.Rows)
	width := 20 + float64(n)*nodeW + float64(n-1)*gap + 20
	height := top + nodeH + 150

	x := func(i int) float64 { return 20 + float64(i)*(nodeW+gap) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="pmap" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-label="%s process map">`,
		width, height, width, height, html.EscapeString(ps.Label))
	b.WriteString(`<defs><marker id="ah" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z"/></marker>` +
		`<marker id="ahl" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="loop" d="M0,0 L10,5 L0,10 z"/></marker></defs>`)

	maxCount := 1
	for _, e := range ps.Edges {
		if e.Count > maxCount {
			maxCount = e.Count
		}
	}
	link := func(path string, kv ...string) string { return html.EscapeString(scope.URL(path, kv...)) }

	// edges
	fwd, back := 0, 0
	for _, e := range ps.Edges {
		if e.FromIndex == e.ToIndex {
			continue
		}
		sw := 1.5 + 5*float64(e.Count)/float64(maxCount)
		label := fmt.Sprintf("%d · %s", e.Count, fmtHours(e.Delay.Median))
		href := link("/command/handoffs", "workflow", ps.Module, "tfrom", e.From, "tto", e.To)
		cls := "edge"
		switch {
		case e.Backward:
			cls = "edge loop"
		case e.Fallout:
			cls = "edge fall"
		}
		x1 := x(e.FromIndex) + nodeW/2
		x2 := x(e.ToIndex) + nodeW/2
		var d string
		var lx, ly float64
		switch {
		case !e.Backward && e.ToIndex == e.FromIndex+1:
			y := top + nodeH/2
			d = fmt.Sprintf("M%.0f,%.0f L%.0f,%.0f", x(e.FromIndex)+nodeW, y, x(e.ToIndex)-4, y)
			lx, ly = (x(e.FromIndex)+nodeW+x(e.ToIndex))/2, y-14
		case !e.Backward:
			fwd++
			lift := 18.0 + float64(fwd)*16
			d = fmt.Sprintf("M%.0f,%.0f C%.0f,%.0f %.0f,%.0f %.0f,%.0f", x1, top, x1, top-lift*1.6, x2, top-lift*1.6, x2, top-4)
			lx, ly = (x1+x2)/2, top-lift*1.2-6
		default:
			back++
			drop := 22.0 + float64(back)*18
			yb := top + nodeH
			d = fmt.Sprintf("M%.0f,%.0f C%.0f,%.0f %.0f,%.0f %.0f,%.0f", x1, yb, x1, yb+drop*1.6, x2, yb+drop*1.6, x2, yb+4)
			lx, ly = (x1+x2)/2, yb+drop*1.2+14
			label = fmt.Sprintf("loop %d · %s", e.Count, fmtHours(e.Delay.Median))
		}
		marker := "ah"
		if e.Backward {
			marker = "ahl"
		}
		fmt.Fprintf(&b, `<a href="%s"><title>%s → %s: %d moves, median %s in %s before moving%s</title><path class="%s" d="%s" stroke-width="%.1f" marker-end="url(#%s)"/>`+
			`<text class="elabel" x="%.0f" y="%.0f" text-anchor="middle">%s</text></a>`,
			href, html.EscapeString(e.From), html.EscapeString(e.To), e.Count, fmtHours(e.Delay.Median), html.EscapeString(e.From),
			map[bool]string{true: " (rework loop)", false: ""}[e.Backward], cls, d, sw, marker, lx, ly, html.EscapeString(label))
	}

	// nodes
	for i, r := range ps.Rows {
		cls := bandClass(r.Band)
		if r.Terminal {
			cls = "term"
		}
		href := link("/command/stage", "workflow", ps.Module, "stage", r.Status)
		fmt.Fprintf(&b, `<a href="%s"><title>%s · %s</title><g class="node %s"><rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="3"/>`,
			href, html.EscapeString(ps.Label), html.EscapeString(r.Status), cls, x(i), top, nodeW, nodeH)
		fmt.Fprintf(&b, `<text class="nname" x="%.0f" y="%.0f">%s</text>`, x(i)+12, top+22, html.EscapeString(truncate(r.Status, 16)))
		if r.Terminal {
			fmt.Fprintf(&b, `<text class="nstat" x="%.0f" y="%.0f">%d reached</text>`, x(i)+12, top+46, r.Entered)
		} else {
			fmt.Fprintf(&b, `<text class="nstat" x="%.0f" y="%.0f">WIP %d · med %s</text>`, x(i)+12, top+46, r.WIP, fmtHours(r.Time.Median))
			sla := "no SLA"
			if r.HasTarget {
				sla = fmt.Sprintf("SLA %.0f%% · %d br", r.Compliance, r.Breaches)
			}
			fmt.Fprintf(&b, `<text class="nstat" x="%.0f" y="%.0f">%s</text>`, x(i)+12, top+66, html.EscapeString(sla))
			fmt.Fprintf(&b, `<text class="nstat" x="%.0f" y="%.0f">%.1f/day out</text>`, x(i)+12, top+84, r.OutRate)
			if r.Band != "—" && r.Band != "" {
				fmt.Fprintf(&b, `<text class="nband %s" x="%.0f" y="%.0f" text-anchor="end">%s %.0f</text>`, cls, x(i)+nodeW-10, top+nodeH-10, html.EscapeString(strings.ToUpper(r.Band)), r.Severity)
			}
		}
		b.WriteString(`</g></a>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// barChart draws started (muted) and completed (teal) bars with the backlog
// line (amber) over the throughput intervals
func barChart(v ThroughputView) template.HTML {
	n := len(v.Rows)
	if n == 0 || v.Max == 0 {
		return ""
	}
	const h, pad = 200.0, 24.0
	bw := math.Max(6, math.Min(36, 900/float64(n)))
	width := pad*2 + float64(n)*bw
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="bars" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-label="Started, completed and backlog per interval">`, width, h+30, width, h+30)
	for i, r := range v.Rows {
		x := pad + float64(i)*bw
		hs := float64(r.Started) / float64(v.Max) * (h - 20)
		hc := float64(r.Completed) / float64(v.Max) * (h - 20)
		fmt.Fprintf(&b, `<a href="%s"><title>%s: %d started, %d completed, backlog %d</title>`, html.EscapeString(r.Link), html.EscapeString(r.Label), r.Started, r.Completed, r.Backlog)
		fmt.Fprintf(&b, `<rect class="st" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, x+1, h-hs, bw/2-1, hs)
		fmt.Fprintf(&b, `<rect class="co" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/></a>`, x+bw/2, h-hc, bw/2-1, hc)
		if n <= 16 || i%int(math.Ceil(float64(n)/12)) == 0 {
			fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.0f">%s</text>`, x, h+16, html.EscapeString(r.Label))
		}
	}
	if v.MaxWIP > 0 {
		var pts []string
		for i, r := range v.Rows {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", pad+float64(i)*bw+bw/2, h-float64(r.Backlog)/float64(v.MaxWIP)*(h-30)))
		}
		fmt.Fprintf(&b, `<polyline class="wip" points="%s"/>`, strings.Join(pts, " "))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// lineChart draws a series with an optional target line and a marker where
// a test or goal started
func lineChart(series []float64, target float64, hasTarget bool, marker int, unit string) template.HTML {
	const w, h, pad = 640.0, 180.0, 28.0
	lo, hi := math.Inf(1), math.Inf(-1)
	n := 0
	for _, v := range series {
		if !math.IsNaN(v) {
			n++
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if hasTarget {
		lo, hi = math.Min(lo, target), math.Max(hi, target)
	}
	if n < 2 {
		return template.HTML(`<p class="deck-note">Not enough weekly measurements yet to draw a trend.</p>`)
	}
	if hi == lo {
		hi, lo = hi+1, lo-1
	}
	span := hi - lo
	floor := lo >= 0
	lo -= span * 0.1
	hi += span * 0.1
	if floor && lo < 0 {
		lo = 0
	}
	step := (w - 2*pad) / float64(max(1, len(series)-1))
	y := func(v float64) float64 { return h - pad - (v-lo)/(hi-lo)*(h-2*pad) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="line" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="Weekly trend">`, w, h)
	fmt.Fprintf(&b, `<text class="axis" x="4" y="%.0f">%s</text><text class="axis" x="4" y="%.0f">%s</text>`, pad-6, fmtMetric(hi, unit), h-6, fmtMetric(lo, unit))
	if hasTarget {
		fmt.Fprintf(&b, `<line class="target" x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f"/><text class="axis tgt" x="%.0f" y="%.1f" text-anchor="end">target %s</text>`,
			pad, w-pad, y(target), y(target), w-pad, y(target)-4, fmtMetric(target, unit))
	}
	if marker > 0 && marker < len(series) {
		mx := pad + float64(marker)*step
		fmt.Fprintf(&b, `<line class="mark" x1="%.1f" x2="%.1f" y1="%.0f" y2="%.0f"/><text class="axis" x="%.1f" y="%.0f">start</text>`, mx, mx, pad-10, h-pad, mx+4, pad)
	}
	var pts []string
	for i, v := range series {
		if math.IsNaN(v) {
			if len(pts) > 1 {
				fmt.Fprintf(&b, `<polyline points="%s"/>`, strings.Join(pts, " "))
			}
			pts = nil
			continue
		}
		px, py := pad+float64(i)*step, y(v)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", px, py))
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.5"><title>%s</title></circle>`, px, py, fmtMetric(v, unit))
	}
	if len(pts) > 1 {
		fmt.Fprintf(&b, `<polyline points="%s"/>`, strings.Join(pts, " "))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// scopeHidden renders the scope as hidden inputs, for forms that must keep it
func scopeHidden(s Scope, skip ...string) template.HTML {
	var b strings.Builder
	for k, vv := range s.Values() {
		if contains(skip, k) {
			continue
		}
		for _, v := range vv {
			fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(k), html.EscapeString(v))
		}
	}
	return template.HTML(b.String())
}
