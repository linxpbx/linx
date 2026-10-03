package push

import (
	"fmt"
	"net/http"
	"strings"
)

// Stats is what the gateway has done since the control plane started. It is
// served in Prometheus text form on the private metrics endpoint, next to
// the certificate ones, so "push → ringing" can be watched rather than
// guessed (docs/PHASE2.md §14).
type Stats struct {
	Sent   map[string]int
	Failed map[string]int
	// Dead: tokens Apple refused as no longer good, and Linx forgot.
	Dead int
	// Limited: pushes not sent because one phone was being sent too many.
	Limited int
	// Woke, WokeSeconds and SlowestWake measure the thing that matters:
	// how long from asking Apple to wake a phone to that phone being
	// signed in and ringable.
	Woke        int
	WokeSeconds float64
	SlowestWake float64
}

func (g *Gateway) count(f func(*Stats)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stats.Sent == nil {
		g.stats.Sent, g.stats.Failed = map[string]int{}, map[string]int{}
	}
	f(&g.stats)
}

// Snapshot is the stats as they stand.
func (g *Gateway) Snapshot() Stats {
	g.init()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.stats
	out.Sent, out.Failed = copyCounts(g.stats.Sent), copyCounts(g.stats.Failed)
	return out
}

func copyCounts(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// WriteMetrics writes the gateway's lines in Prometheus text format.
func (g *Gateway) WriteMetrics(b *strings.Builder) {
	s := g.Snapshot()
	fmt.Fprintf(b, "# HELP linx_push_sent_total Pushes Apple accepted, by what they were for.\n")
	fmt.Fprintf(b, "# TYPE linx_push_sent_total counter\n")
	for _, kind := range []string{KindWake, KindMissed, KindVoicemail} {
		fmt.Fprintf(b, "linx_push_sent_total{kind=%q} %d\n", kind, s.Sent[kind])
	}
	fmt.Fprintf(b, "# HELP linx_push_failed_total Pushes that didn't go out.\n")
	fmt.Fprintf(b, "# TYPE linx_push_failed_total counter\n")
	for _, kind := range []string{KindWake, KindMissed, KindVoicemail} {
		fmt.Fprintf(b, "linx_push_failed_total{kind=%q} %d\n", kind, s.Failed[kind])
	}
	fmt.Fprintf(b, "# HELP linx_push_dead_tokens_total Push tokens Apple refused as no longer good.\n")
	fmt.Fprintf(b, "# TYPE linx_push_dead_tokens_total counter\nlinx_push_dead_tokens_total %d\n", s.Dead)
	fmt.Fprintf(b, "# HELP linx_push_limited_total Pushes held back by one phone's rate limit.\n")
	fmt.Fprintf(b, "# TYPE linx_push_limited_total counter\nlinx_push_limited_total %d\n", s.Limited)
	fmt.Fprintf(b, "# HELP linx_push_wake_seconds_total Seconds from waking a phone to it being signed in.\n")
	fmt.Fprintf(b, "# TYPE linx_push_wake_seconds_total counter\nlinx_push_wake_seconds_total %.3f\n", s.WokeSeconds)
	fmt.Fprintf(b, "# HELP linx_push_wake_total Phones that arrived after being woken.\n")
	fmt.Fprintf(b, "# TYPE linx_push_wake_total counter\nlinx_push_wake_total %d\n", s.Woke)
	fmt.Fprintf(b, "# HELP linx_push_wake_slowest_seconds The slowest wake since this server started.\n")
	fmt.Fprintf(b, "# TYPE linx_push_wake_slowest_seconds gauge\nlinx_push_wake_slowest_seconds %.3f\n", s.SlowestWake)
}

// MetricsHandler serves only the push lines (the control plane mounts it on
// its private metrics endpoint).
func (g *Gateway) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var b strings.Builder
		g.WriteMetrics(&b)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(b.String()))
	})
}
