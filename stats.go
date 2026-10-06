// Key figures over the request log.

package main

import (
	"sort"
	"time"
)

type dist struct {
	n                  int
	avg, p50, p95, max float64
}

func newDist(v []float64) dist {
	if len(v) == 0 {
		return dist{}
	}
	sort.Float64s(v)
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	q := func(p float64) float64 { return v[int(p*float64(len(v)-1)+0.5)] }
	return dist{n: len(v), avg: sum / float64(len(v)), p50: q(0.5), p95: q(0.95), max: v[len(v)-1]}
}

type reqStats struct {
	n                            int           // basis: requests in the log
	window                       time.Duration // window for the rates, 0 = too short
	perMin, promptMin, genMin    float64
	ttft, dur, rate, prompt, gen dist
}

const statWindow = 5 * time.Minute

func (m *monitor) stats(now time.Time) reqStats {
	st := reqStats{n: len(m.log)}
	if d := now.Sub(m.since); d >= 10*time.Second {
		st.window = min(d, statWindow)
	}
	var ttft, dur, rate, prompt, gen []float64
	var cnt, pt, gt int
	for _, r := range m.log {
		if st.window > 0 && now.Sub(r.end) <= st.window {
			cnt++
			pt += r.prompt
			gt += r.gen
		}
		prompt = append(prompt, float64(r.prompt))
		if r.ttft > 0 {
			ttft = append(ttft, r.ttft.Seconds())
		}
		// Partial requests have no duration; with dur == 0 otherwise even the
		// number of generated tokens is unknown.
		if r.dur > 0 {
			dur = append(dur, r.dur.Seconds())
		}
		if r.genKnown() {
			gen = append(gen, float64(r.gen))
		}
		if r.rate > 0 {
			rate = append(rate, r.rate)
		}
	}
	if st.window > 0 {
		mins := st.window.Minutes()
		st.perMin, st.promptMin, st.genMin = float64(cnt)/mins, float64(pt)/mins, float64(gt)/mins
	}
	st.ttft, st.dur, st.rate = newDist(ttft), newDist(dur), newDist(rate)
	st.prompt, st.gen = newDist(prompt), newDist(gen)
	return st
}
