package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/elvonpiko/mymo/internal/facts"
)

// liveEvery is the sampling cadence: one `cat` of three proc files over
// the open connection — cheap enough to feel alive, quiet enough to
// never be noise.
const liveEvery = 2 * time.Second

// liveSample is one snapshot of a node's moving numbers.
type liveSample struct {
	load     string // 1-minute load average
	memAvail uint64
	memTotal uint64
	busy     uint64 // /proc/stat jiffies spent on work
	idle     uint64 // idle + iowait jiffies
	total    uint64 // all jiffies
}

// Live messages carry the session they belong to. A stale message from
// a superseded session is ignored instead of corrupting a newer one.
type liveSampleMsg struct {
	seq int
	s   liveSample
}

type liveErrMsg struct {
	seq int
	err error
}

// stopLive ends sampling and drops the connection; the next entry
// opens a fresh one.
func (m *Model) stopLive() {
	m.live = false
	if m.liveClient != nil {
		m.liveClient.Close()
		m.liveClient = nil
	}
	m.liveErr = ""
	m.liveCPU = -1
	m.liveSparkCPU = m.liveSparkCPU[:0]
	m.liveSparkMem = m.liveSparkMem[:0]
}

// sampleLive takes one sample over the open connection.
func (m Model) sampleLive(seq int) tea.Cmd {
	client := m.liveClient
	return func() tea.Msg {
		if client == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _, err := client.Run(ctx, "cat", "/proc/loadavg", "/proc/meminfo", "/proc/stat")
		if err != nil {
			return liveErrMsg{seq: seq, err: err}
		}
		return liveSampleMsg{seq: seq, s: parseLive(out)}
	}
}

// handleLiveSample records one sample: cpu percent from jiffy deltas,
// memory headroom, load — cpu and mem each keep a short sparkline
// history. The next sample is scheduled only while live stays on.
func (m Model) handleLiveSample(msg liveSampleMsg) (tea.Model, tea.Cmd) {
	if !m.live || msg.seq != m.liveSeq || m.liveClient == nil {
		return m, nil
	}
	s := msg.s
	if s.total > m.livePrev.total && m.livePrev.total > 0 {
		dTotal := s.total - m.livePrev.total
		dIdle := s.idle - m.livePrev.idle
		m.liveCPU = clampInt(100-int((dIdle*100)/dTotal), 0, 100)
	}
	m.livePrev = s
	m.liveCur = s
	if s.memTotal > 0 {
		used := int(((s.memTotal - s.memAvail) * 100) / s.memTotal)
		m.liveSparkMem = appendSpark(m.liveSparkMem, clampInt(used, 0, 100))
		if m.liveCPU >= 0 {
			m.liveSparkCPU = appendSpark(m.liveSparkCPU, m.liveCPU)
		}
	}
	return m, tea.Tick(liveEvery, func(time.Time) tea.Msg {
		return m.sampleLive(msg.seq)()
	})
}

// handleLiveErr stops sampling honestly: the line says why it stopped,
// and never pretends stale numbers are fresh. Silent retries would be
// guessing — leaving and re-entering observes again.
func (m Model) handleLiveErr(msg liveErrMsg) (tea.Model, tea.Cmd) {
	if !m.live || msg.seq != m.liveSeq {
		return m, nil
	}
	m.live = false
	if m.liveClient != nil {
		m.liveClient.Close()
		m.liveClient = nil
	}
	m.liveErr = shorten(strings.TrimPrefix(msg.err.Error(), "ssh: "), 30)
	return m, nil
}

// liveLine renders the card's live row: the green pulse dot and the
// node's moving numbers. A stopped line keeps showing the last known
// memory, labeled as a snapshot — honest about what is live and what
// is not.
func (m Model) liveLine() string {
	if m.live && m.liveClient != nil {
		cpu := "warming"
		if m.liveCPU >= 0 {
			cpu = codeStyle.Render(spark(m.liveSparkCPU)) + " " + textStyle.Render(strconv.Itoa(m.liveCPU)+"%")
		}
		return okStyle.Render("● live") + "  " +
			liveMetric("cpu", cpu) + "  " +
			liveMetric("mem", codeStyle.Render(spark(m.liveSparkMem))+" "+textStyle.Render(m.memLiveText())) + "  " +
			liveMetric("load", textStyle.Render(orDash(m.liveCur.load)))
	}
	if m.liveErr != "" {
		return faintStyle.Render("● live stopped · "+m.liveErr) + "  " +
			faintStyle.Render("mem "+m.memLiveText()+" (snapshot)")
	}
	return faintStyle.Render("mem " + m.memLiveText() + " (snapshot)")
}

// liveMetric renders one "label value" group.
func liveMetric(label, value string) string {
	return faintStyle.Render(label+" ") + value
}

// memLiveText renders the memory readout: "3.2/3.8 GiB avail".
func (m Model) memLiveText() string {
	s := m.liveCur
	if s.memTotal == 0 {
		return "unknown"
	}
	total := facts.FormatBytes(s.memTotal)
	if s.memAvail == 0 {
		return total
	}
	return facts.FormatBytes(s.memAvail) + "/" + total + " avail"
}

// parseLive extracts the moving numbers from the concatenated output
// of `cat /proc/loadavg /proc/meminfo /proc/stat`. Missing pieces
// stay zero — the display names them unknown, never guesses.
func parseLive(out string) liveSample {
	var s liveSample
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case s.load == "" && strings.Contains(line, "/"):
			// loadavg: "0.08 0.10 0.09 1/123 45678"
			if _, err := strconv.ParseFloat(fields[0], 64); err == nil {
				s.load = fields[0]
			}
		case fields[0] == "MemTotal:":
			s.memTotal = memKBytes(fields)
		case fields[0] == "MemAvailable:":
			s.memAvail = memKBytes(fields)
		case fields[0] == "cpu" && s.total == 0:
			// cpu user nice system idle iowait irq softirq steal guest guest_nice
			var vals [10]uint64
			for i, f := range fields[1:] {
				if i >= len(vals) {
					break
				}
				v, err := strconv.ParseUint(f, 10, 64)
				if err != nil {
					continue
				}
				vals[i] = v
			}
			s.busy = vals[0] + vals[1] + vals[2] + vals[5] + vals[6] + vals[7]
			s.idle = vals[3] + vals[4]
			s.total = s.busy + s.idle + vals[8] + vals[9]
		}
	}
	return s
}

// memKBytes parses meminfo's "N kB" value into bytes.
func memKBytes(fields []string) uint64 {
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return v * 1024
}

// sparkBlocks are the 8 vertical levels a sparkline can show.
const sparkBlocks = "▁▂▃▄▅▆▇█"

// spark renders the last few samples as a tiny bar sequence.
func spark(samples []int) string {
	if len(samples) == 0 {
		return ""
	}
	start := 0
	if len(samples) > 8 {
		start = len(samples) - 8
	}
	blocks := []rune(sparkBlocks)
	var b strings.Builder
	for _, v := range samples[start:] {
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		b.WriteRune(blocks[v*(len(blocks)-1)/100])
	}
	return b.String()
}

// appendSpark keeps a bounded history.
func appendSpark(samples []int, v int) []int {
	samples = append(samples, v)
	if len(samples) > 32 {
		samples = samples[len(samples)-32:]
	}
	return samples
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
