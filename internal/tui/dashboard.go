package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/pipeline"
	"github.com/gabrigas/aniup/internal/probe"
)

const TelemetryInterval = 2 * time.Second

// ProgressMsg wraps a real-time pipeline.Event for the Bubble Tea update loop.
type ProgressMsg struct {
	Event pipeline.Event
}

// TelemetryMsg carries periodic /dev/shm and GPU VRAM/temperature metrics.
type TelemetryMsg struct {
	ShmUsedBytes uint64
	VRAMUsedMiB  int
	GPUTempC     int
	GPUAvailable bool
}

// TelemetryReader abstracts GPU and /dev/shm resource polling.
type TelemetryReader interface {
	ReadGPUStats(ctx context.Context) (vramMiB int, tempC int, err error)
	ReadDirBytes(path string) uint64
}

// OSTelemetryReader polls nvidia-smi and walks /dev/shm/aniup_<pid> for actual usage.
type OSTelemetryReader struct {
	NvidiaSMIPath string
}

// ReadGPUStats queries nvidia-smi for VRAM usage (MiB) and GPU temperature (°C).
// Example: vram, temp, err := reader.ReadGPUStats(ctx)
func (r *OSTelemetryReader) ReadGPUStats(ctx context.Context) (int, int, error) {
	bin := r.NvidiaSMIPath
	if bin == "" {
		bin = "nvidia-smi"
	}
	cmd := exec.CommandContext(ctx, bin, "--query-gpu=memory.used,temperature.gpu", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	return ParseNvidiaSMIOutput(string(out))
}

// ReadDirBytes sums the file sizes inside path (used for /dev/shm telemetry).
// Example: used := reader.ReadDirBytes("/dev/shm/aniup_1234")
func (r *OSTelemetryReader) ReadDirBytes(path string) uint64 {
	if path == "" {
		return 0
	}
	var total uint64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				total += uint64(info.Size())
			}
		}
		return nil
	})
	return total
}

// ParseNvidiaSMIOutput parses the "memory.used, temperature.gpu" CSV line from nvidia-smi.
// Example: vram, temp, err := tui.ParseNvidiaSMIOutput("3420, 62\n")
func ParseNvidiaSMIOutput(csvOut string) (int, int, error) {
	firstLine := strings.Split(strings.TrimSpace(csvOut), "\n")[0]
	parts := strings.Split(firstLine, ",")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid nvidia-smi CSV output %q: expected 'vram, temp'", csvOut)
	}
	vram, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	temp, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("failed to parse nvidia-smi numbers from %q", csvOut)
	}
	return vram, temp, nil
}

// DashboardModel is the Bubble Tea model for Phase 4 real-time pipeline execution.
type DashboardModel struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cfg        config.RunConfig
	meta       probe.VideoMetadata
	events     <-chan pipeline.Event
	telemetry  TelemetryReader
	startTime  time.Time
	globalProg progress.Model
	chunkProg  progress.Model
	spin       spinner.Model
	lastEvent  pipeline.Event
	lastTelem  TelemetryMsg
	aborted    bool
}

// NewDashboardModel constructs a Bubble Tea DashboardModel.
// Example: m := tui.NewDashboardModel(ctx, cancel, cfg, meta, events, telemetry)
func NewDashboardModel(
	ctx context.Context,
	cancel context.CancelFunc,
	cfg config.RunConfig,
	meta probe.VideoMetadata,
	events <-chan pipeline.Event,
	telemetry TelemetryReader,
) DashboardModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return DashboardModel{
		ctx:        ctx,
		cancel:     cancel,
		cfg:        cfg,
		meta:       meta,
		events:     events,
		telemetry:  telemetry,
		startTime:  time.Now(),
		globalProg: progress.New(progress.WithDefaultGradient()),
		chunkProg:  progress.New(progress.WithSolidFill("#04B575")),
		spin:       sp,
	}
}

// Init starts the spinner, pipeline event listener, and 2-second telemetry ticker.
func (m DashboardModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, waitForPipelineEvent(m.events), tickTelemetry(m.ctx, m.telemetry, m.lastEvent.ShmWorkspace))
}

// Update processes pipeline events, telemetry updates, spinner ticks, and graceful shutdown keys.
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		if typed.String() == "q" || typed.String() == "ctrl+c" {
			m.aborted = true
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	case ProgressMsg:
		m.lastEvent = typed.Event
		if typed.Event.Done || typed.Event.Err != nil {
			return m, tea.Quit
		}
		return m, waitForPipelineEvent(m.events)
	case TelemetryMsg:
		m.lastTelem = typed
		return m, tickTelemetry(m.ctx, m.telemetry, m.lastEvent.ShmWorkspace)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(typed)
		return m, cmd
	}
	return m, nil
}

// View renders the styled dashboard with Header, Global Progress, Chunk Stage Progress, and Telemetry.
func (m DashboardModel) View() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString(m.renderGlobalProgress())
	b.WriteString("\n\n")
	b.WriteString(m.renderChunkProgress())
	b.WriteString("\n\n")
	b.WriteString(m.renderTelemetry())

	if m.aborted {
		b.WriteString("\n\n")
		b.WriteString(warnBadgeStyle.Render("⚠ Interrupção solicitada: limpando /dev/shm... O progresso até o chunk anterior está salvo no checkpoint!"))
	} else if m.lastEvent.Err != nil {
		b.WriteString("\n\n")
		b.WriteString(errBadgeStyle.Render(fmt.Sprintf("✗ Erro: %v (progresso até o chunk anterior salvo)", m.lastEvent.Err)))
	} else if m.lastEvent.Done {
		b.WriteString("\n\n")
		b.WriteString(okBadgeStyle.Render(fmt.Sprintf("✓ Concluído com sucesso! Arquivo salvo em: %s", m.cfg.OutputPath)))
	}
	return doctorBoxStyle.Render(b.String())
}

func (m DashboardModel) renderHeader() string {
	targetW := m.meta.Width * m.cfg.Scale
	targetH := m.meta.Height * m.cfg.Scale
	title := doctorTitleStyle.Render("aniup — Upscaling em RAM (/dev/shm)")
	info := fmt.Sprintf(
		"Arquivo : %s\nResolução: %dx%d -> %dx%d (%dx) | Engine: %s (%s) | GPU Profile: %s",
		filepath.Base(m.cfg.InputPath),
		m.meta.Width, m.meta.Height, targetW, targetH, m.cfg.Scale,
		m.cfg.Engine, m.cfg.Model, m.cfg.Profile,
	)
	return title + "\n" + info
}

func (m DashboardModel) renderGlobalProgress() string {
	totalChunks := max(m.lastEvent.TotalChunks, 1)
	totalFrames := max(m.lastEvent.TotalFrames, m.meta.TotalFrames, 1)
	ratio := float64(m.lastEvent.FramesDone) / float64(totalFrames)
	elapsed := max(time.Since(m.startTime).Seconds(), 0.1)
	avgFPS := float64(m.lastEvent.FramesDone) / elapsed
	var etaStr string
	if avgFPS > 0.01 && m.lastEvent.FramesDone < totalFrames {
		remSec := float64(totalFrames-m.lastEvent.FramesDone) / avgFPS
		etaStr = (time.Duration(remSec) * time.Second).Round(time.Second).String()
	} else {
		etaStr = "--"
	}
	label := fmt.Sprintf(
		"Progresso Global: %d/%d chunks (%.1f%%) | Velocidade: %.2f FPS | ETA: %s",
		m.lastEvent.CompletedChunks, totalChunks, ratio*100, avgFPS, etaStr,
	)
	return label + "\n" + m.globalProg.ViewAs(ratio)
}

func (m DashboardModel) renderChunkProgress() string {
	stageBadge := formatStageBadge(m.lastEvent.Stage, m.lastEvent.StagePercent)
	ratio := m.lastEvent.StagePercent / 100.0
	label := fmt.Sprintf("%s Chunk #%d: %s", m.spin.View(), m.lastEvent.ChunkIndex+1, stageBadge)
	return label + "\n" + m.chunkProg.ViewAs(ratio)
}

func (m DashboardModel) renderTelemetry() string {
	shmMiB := float64(m.lastTelem.ShmUsedBytes) / (1024 * 1024)
	gpuInfo := "VRAM: N/A | Temp: N/A"
	if m.lastTelem.GPUAvailable {
		gpuInfo = fmt.Sprintf("VRAM: %d MiB | Temp: %d°C", m.lastTelem.VRAMUsedMiB, m.lastTelem.GPUTempC)
	}
	return fmt.Sprintf("Telemetria: /dev/shm em uso: %.1f MiB | %s | [q / Ctrl+C para pausar com segurança]", shmMiB, gpuInfo)
}

func formatStageBadge(stage pipeline.StageType, pct float64) string {
	switch stage {
	case pipeline.StageExtracting:
		return "[1/3 Extraindo]"
	case pipeline.StageUpscaling:
		return fmt.Sprintf("[2/3 Upscaling %.1f%%]", pct)
	case pipeline.StageEncoding:
		return "[3/3 Codificando NVENC]"
	case pipeline.StageConcatenating:
		return "[Concatenando MKV Final]"
	default:
		return "[Iniciando...]"
	}
}

func waitForPipelineEvent(events <-chan pipeline.Event) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return ProgressMsg{Event: pipeline.Event{Done: true}}
		}
		return ProgressMsg{Event: ev}
	}
}

func tickTelemetry(ctx context.Context, reader TelemetryReader, shmPath string) tea.Cmd {
	if reader == nil {
		return nil
	}
	return tea.Tick(TelemetryInterval, func(_ time.Time) tea.Msg {
		used := reader.ReadDirBytes(shmPath)
		vram, temp, err := reader.ReadGPUStats(ctx)
		return TelemetryMsg{
			ShmUsedBytes: used,
			VRAMUsedMiB:  vram,
			GPUTempC:     temp,
			GPUAvailable: err == nil,
		}
	})
}
