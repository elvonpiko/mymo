package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"

	"github.com/elvonpiko/mymo/internal/facts"
)

// liveEvery is the sampling cadence: one `cat` of four proc files and
// one `docker ps` over the open connection — cheap enough to feel
// alive, quiet enough to never be noise.
const liveEvery = 2 * time.Second

// animEvery drives the harmonica springs: every tick advances each
// spring one frame toward its target, so a gauge's number settles
// like a needle sweeping to its reading — never a jump.
const animEvery = 66 * time.Millisecond

// liveSpring is the frame the gauges animate at: FPS(15) matches
// animEvery, critically damped so numbers sweep up and settle
// without oscillating. Angular frequency 8 settles a reading in
// roughly two thirds of a second.
var liveSpring = harmonica.NewSpring(harmonica.FPS(15), 8.0, 1.0)

// liveSample is one snapshot of a node's moving numbers.
type liveSample struct {
	load     string // 1-minute load average
	load5    string // 5-minute
	load15   string // 15-minute
	memAvail uint64
	memTotal uint64
	busy     uint64 // /proc/stat jiffies spent on work
	idle     uint64 // idle + iowait
	total    uint64 // all jiffies
	rxBytes  uint64 // /proc/net/dev bytes received, lo excluded
	txBytes  uint64 // bytes transmitted, lo excluded
	at       time.Time
}

// Live messages carry the session they belong to. A stale message from
// a superseded session is ignored instead of corrupting a newer one.
type liveSampleMsg struct {
	seq     int
	s       liveSample
	dockOK  bool     // docker answered this sample
	dockers []string // running containers, by name
}

// liveErrMsg reports a failed sample; the card states it and stops
// pretending to be live.
type liveErrMsg struct {
	seq int
	err error
}

// animTickMsg advances every spring one frame while something is
// still settling.
type animTickMsg struct{ seq int }

// animValue is one harmonica-driven number: the position and velocity
// the spring keeps updating toward the target. A value that never
// received a target does not render — unknown stays unknown.
type animValue struct {
	pos, vel, target float64
	has              bool
}

// step advances the spring one frame and reports whether it moved.
func (a *animValue) step() {
	if !a.has {
		return
	}
	a.pos, a.vel = liveSpring.Update(a.pos, a.vel, a.target)
}

// settled reports whether the spring has come to rest at its target.
func (a animValue) settled() bool {
	return !a.has || (abs(a.pos-a.target) < 0.4 && abs(a.vel) < 0.4)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// animState carries the springs behind the LIVE card's numbers and
// whether a tick is in flight — the ticker runs only while something
// is still moving, so a calm card costs nothing.
type animState struct {
	cpu, mem, rx, tx animValue
	ticking          bool
}

// aim points a value at its new reading and wakes the ticker once;
// further aims ride the tick already in flight.
func (a *animState) aim(v *animValue, target float64, seq int) tea.Cmd {
	v.target = target
	v.has = true
	if a.ticking {
		return nil
	}
	a.ticking = true
	return tea.Tick(animEvery, func(time.Time) tea.Msg {
		return animTickMsg{seq: seq}
	})
}

// pauseLive stops scheduling live samples but keeps the connection
// open: the preflight audit and plan generation borrow it, and a
// racing tick that times out would close the connection under their
// feet. Results of in-flight samples are ignored until sampling
// resumes.
func (m *Model) pauseLive() {
	m.live = false
	m.livePaused = true
}

// resumeLive restarts sampling over the kept connection, resuming
// the same session's sequence.
func (m *Model) resumeLive() tea.Cmd {
	m.livePaused = false
	if m.liveClient == nil {
		return nil
	}
	m.live = true
	return m.sampleLive(m.liveSeq)
}

// maybeResumeLive restarts sampling only when the observe page is the
// resting screen, no ceremony is running, and no audit still holds
// the connection in the background.
func (m *Model) maybeResumeLive() tea.Cmd {
	if !m.livePaused || m.loading.active || m.auditBusy || m.cur().kind != scNode || m.liveClient == nil {
		return nil
	}
	return m.resumeLive()
}

// stopLive ends sampling and drops the connection; the next entry
// opens a fresh one.
func (m *Model) stopLive() {
	m.live = false
	m.livePaused = false
	if m.liveClient != nil {
		m.liveClient.Close()
		m.liveClient = nil
	}
	m.liveErr = ""
	m.liveCPU = -1
	m.liveSparkCPU = m.liveSparkCPU[:0]
	m.liveSparkMem = m.liveSparkMem[:0]
	m.liveNetRx, m.liveNetTx = 0, 0
	m.liveDocker = nil
	m.liveDockOK = false
	m.liveAnim = animState{}
}

// sampleLive takes one sample over the open connection: the proc
// files for the box's motion, docker for what is running on it.
func (m Model) sampleLive(seq int) tea.Cmd {
	client := m.liveClient
	return func() tea.Msg {
		if client == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, _, err := client.Run(ctx, "cat", "/proc/loadavg", "/proc/meminfo", "/proc/stat", "/proc/net/dev")
		if err != nil {
			return liveErrMsg{seq: seq, err: err}
		}
		s := parseLive(out)
		s.at = time.Now()
		msg := liveSampleMsg{seq: seq, s: s}
		// docker is sampled in the same breath: a refused call means
		// no docker to show, never a guess
		if dock, _, derr := client.Run(ctx, "docker", "ps", "--format", "{{.Names}}\t{{.Status}}"); derr == nil {
			msg.dockOK = true
			msg.dockers = parseDocker(dock)
		}
		return msg
	}
}

// handleLiveSample records one sample: cpu percent from jiffy deltas,
// memory headroom, load, network rates from counter deltas, the
// running containers — each moving number aims its spring, and the
// next sample is scheduled only while live stays on.
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
	// network rates live between samples: counter deltas over the
	// wall time that passed
	rateOK := false
	if !s.at.IsZero() && !m.livePrev.at.IsZero() {
		dt := s.at.Sub(m.livePrev.at).Seconds()
		if dt > 0 && s.rxBytes >= m.livePrev.rxBytes && s.txBytes >= m.livePrev.txBytes {
			m.liveNetRx = float64(s.rxBytes-m.livePrev.rxBytes) / dt
			m.liveNetTx = float64(s.txBytes-m.livePrev.txBytes) / dt
			rateOK = true
		}
	}
	m.livePrev = s
	m.liveCur = s
	m.liveDocker = msg.dockers
	m.liveDockOK = msg.dockOK
	var aim tea.Cmd
	var cmds []tea.Cmd
	if m.liveCPU >= 0 {
		m.liveSparkCPU = appendSpark(m.liveSparkCPU, m.liveCPU)
		aim = m.liveAnim.aim(&m.liveAnim.cpu, float64(m.liveCPU), msg.seq)
		cmds = append(cmds, aim)
	}
	if s.memTotal > 0 {
		used := int(((s.memTotal - s.memAvail) * 100) / s.memTotal)
		m.liveSparkMem = appendSpark(m.liveSparkMem, clampInt(used, 0, 100))
		aim = m.liveAnim.aim(&m.liveAnim.mem, float64(clampInt(used, 0, 100)), msg.seq)
		cmds = append(cmds, aim)
	}
	if rateOK {
		aim = m.liveAnim.aim(&m.liveAnim.rx, m.liveNetRx, msg.seq)
		cmds = append(cmds, aim)
		aim = m.liveAnim.aim(&m.liveAnim.tx, m.liveNetTx, msg.seq)
		cmds = append(cmds, aim)
	}
	cmds = append(cmds, tea.Tick(liveEvery, func(time.Time) tea.Msg {
		return m.sampleLive(msg.seq)()
	}))
	return m, tea.Batch(cmds...)
}

// handleAnimTick advances every spring one frame and re-arms only
// while something is still settling — an honest card rests.
func (m Model) handleAnimTick(msg animTickMsg) (tea.Model, tea.Cmd) {
	if !m.live || msg.seq != m.liveSeq {
		m.liveAnim.ticking = false
		return m, nil
	}
	a := &m.liveAnim
	a.cpu.step()
	a.mem.step()
	a.rx.step()
	a.tx.step()
	if a.cpu.settled() && a.mem.settled() && a.rx.settled() && a.tx.settled() {
		a.ticking = false
		return m, nil
	}
	return m, tea.Tick(animEvery, func(time.Time) tea.Msg {
		return animTickMsg{seq: msg.seq}
	})
}

// handleLiveErr stops sampling honestly: the card says why it stopped,
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

// liveCardInner renders the LIVE card's contents: cpu with its history
// and heat, network in and out, memory as a filling bar, the load
// averages, what docker is running, and the node's uptime — motion
// lives here, not in SYSTEM. A stopped card keeps showing the last
// known memory and uptime as a snapshot — honest about what is live
// and what is not.
func (m Model) liveCardInner() string {
	if m.live && m.liveClient != nil {
		cpu := faintStyle.Render("warming")
		if m.liveAnim.cpu.has {
			pct := clampInt(int(m.liveAnim.cpu.pos+0.5), 0, 100)
			cpu = codeStyle.Render(spark(m.liveSparkCPU)) + " " +
				cpuHeat(pct).Render(strconv.Itoa(pct)+"%")
		}
		net := faintStyle.Render("warming")
		if m.liveAnim.rx.has {
			net = inStyle.Render("in") + " " + textStyle.Render(rate(m.liveAnim.rx.pos)) + "  " +
				outStyle.Render("out") + " " + textStyle.Render(rate(m.liveAnim.tx.pos))
		}
		mem := faintStyle.Render(m.memLiveText())
		if m.liveAnim.mem.has {
			mem = barStyle.Render(memBar(m.liveAnim.mem.pos)) + " " +
				textStyle.Render(m.memLiveText())
		}
		loads := textStyle.Render(orDash(m.liveCur.load))
		if m.liveCur.load5 != "" {
			loads += "  " + subtextStyle.Render(m.liveCur.load5+"  "+m.liveCur.load15)
		}
		rows := liveCell("CPU", cpu) + "  " + liveCell("NET", net) + "\n" +
			liveCell("MEM", mem) + "  " + liveCell("LOAD", loads) + "\n"
		if m.liveDockOK {
			rows += liveCell("DOCK", m.dockerText()) + "\n"
		}
		rows += liveCell("UP", m.uptimeText())
		return rows
	}
	head := ""
	switch {
	case m.liveErr != "":
		head = faintStyle.Render("● stopped · "+m.liveErr) + "\n"
	default:
		head = faintStyle.Render("● not sampling") + "\n"
	}
	return head + liveCell("MEM", faintStyle.Render(m.memLiveText()+" (snapshot)")) +
		"  " + liveCell("UP", faintStyle.Render(m.uptimeText()))
}

// dockerText renders the running containers: a count, the first few
// names, and an honest fold when there are more.
func (m Model) dockerText() string {
	names := m.liveDocker
	if len(names) == 0 {
		return faintStyle.Render("0 running")
	}
	line := textStyle.Render(strconv.Itoa(len(names))) + " " + textStyle.Render("running") + ": " +
		textStyle.Render(strings.Join(names[:min(3, len(names))], " "))
	if len(names) > 3 {
		line += " " + dimStyle.Render("+"+strconv.Itoa(len(names)-3)+" more")
	}
	return line
}

// cpuHeat colors the cpu reading by how busy the box is: green while
// it breathes, yellow when it works, red when it begs.
func cpuHeat(pct int) lipgloss.Style {
	switch {
	case pct < 50:
		return okStyle
	case pct < 80:
		return warnStyle
	default:
		return errStyle
	}
}

// memBar renders memory as a filling bar: eight segments, the spring's
// position deciding how many are full.
func memBar(pct float64) string {
	full := clampInt(int(pct*8/100), 0, 8)
	return strings.Repeat("▰", full) + strings.Repeat("▱", 8-full)
}

// rate renders a bytes-per-second reading in human units, rounded
// to the nearest byte so a resting spring shows its whole reading.
func rate(bps float64) string {
	if bps < 1 {
		return "0 B/s"
	}
	return facts.FormatBytes(uint64(bps+0.5)) + "/s"
}

// liveCell renders one "LABEL value" group, the label pinned to a
// fixed width so the rows align.
func liveCell(label, value string) string {
	return factsLabelStyle.Width(5).Render(label) + " " + value
}

// memLiveText renders the memory readout: "3.2/3.8 GiB avail". With
// no live sample in flight, the snapshot's memory stands in — the
// caller labels it as such.
func (m Model) memLiveText() string {
	s := m.liveCur
	if s.memTotal == 0 {
		return memText(m.selNode.Facts)
	}
	total := facts.FormatBytes(s.memTotal)
	if s.memAvail == 0 {
		return total
	}
	return facts.FormatBytes(s.memAvail) + "/" + total + " avail"
}

// parseLive extracts the moving numbers from the concatenated output
// of `cat /proc/loadavg /proc/meminfo /proc/stat /proc/net/dev`.
// Missing pieces stay zero — the display names them unknown, never
// guesses.
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
			if _, err := strconv.ParseFloat(fields[0], 64); err != nil || len(fields) < 3 {
				continue
			}
			s.load, s.load5, s.load15 = fields[0], fields[1], fields[2]
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
		default:
			// /proc/net/dev: "eth0: rx_bytes … tx_bytes", the loopback
			// excluded — local chatter is not the box talking to the world
			name := strings.TrimSuffix(fields[0], ":")
			if name == "lo" || !strings.HasSuffix(fields[0], ":") {
				continue
			}
			if len(fields) < 10 {
				continue
			}
			rx, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			tx, err := strconv.ParseUint(fields[9], 10, 64)
			if err != nil {
				continue
			}
			s.rxBytes += rx
			s.txBytes += tx
		}
	}
	return s
}

// parseDocker names the running containers from docker ps's
// name-tab-status lines — "Up …" is running, anything else is not.
func parseDocker(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "Up ") {
			continue
		}
		names = append(names, parts[0])
	}
	return names
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

// liveSparkLen is how many samples a sparkline window shows; the
// history behind it stays longer.
const liveSparkLen = 12

// spark renders the last few samples as a tiny bar sequence.
func spark(samples []int) string {
	if len(samples) == 0 {
		return ""
	}
	start := 0
	if len(samples) > liveSparkLen {
		start = len(samples) - liveSparkLen
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

// uptimeText renders the node's uptime for the LIVE card — "—" when
// the snapshot never learned it, never a guess.
func (m Model) uptimeText() string {
	if m.selNode.Facts.Uptime <= 0 {
		return "—"
	}
	return facts.FormatUptimeShort(m.selNode.Facts.Uptime)
}
