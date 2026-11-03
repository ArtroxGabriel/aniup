package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/gabrigas/aniup/internal/deps"
)

var (
	doctorTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7D56F4")).
				MarginBottom(1)

	doctorBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(1, 2)

	doctorErrBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#FF5F87")).
				Padding(1, 2)

	okBadgeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575"))
	warnBadgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFAF00"))
	errBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F87"))
	cmdCodeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00D7FF"))
)

// RenderDoctorReport formats the CheckReport into a Lipgloss diagnostic box with Arch/CachyOS instructions.
// Example: fmt.Println(tui.RenderDoctorReport(report))
func RenderDoctorReport(report deps.CheckReport) string {
	var b strings.Builder
	b.WriteString(doctorTitleStyle.Render("aniup doctor — Diagnóstico de Ambiente (Arch / CachyOS)"))
	b.WriteString("\n")
	b.WriteString(renderStatusLines(report))

	if report.IsOK() {
		b.WriteString("\n")
		b.WriteString(okBadgeStyle.Render("✓ Ambiente pronto para execução em RAM (/dev/shm) com aceleração GPU!"))
		return doctorBoxStyle.Render(b.String())
	}

	b.WriteString("\n")
	b.WriteString(renderRemediationSection(report.MissingMandatory))
	return doctorErrBoxStyle.Render(b.String())
}

func renderStatusLines(report deps.CheckReport) string {
	var lines []string
	lines = append(lines, formatBinaryStatus("ffmpeg", report.FFmpegPath, true))
	lines = append(lines, formatBinaryStatus("ffprobe", report.FFprobePath, true))
	lines = append(lines, formatBoolStatus("Encoder hevc_nvenc", report.HasHEVCNVENC))
	lines = append(lines, formatBoolStatus("Encoder av1_nvenc", report.HasAV1NVENC))
	lines = append(lines, formatBinaryStatus("realesrgan-ncnn-vulkan", report.RealESRGANPath, report.RealCUGANPath == ""))
	lines = append(lines, formatBinaryStatus("realcugan-ncnn-vulkan", report.RealCUGANPath, report.RealESRGANPath == ""))
	lines = append(lines, formatBinaryStatus("nvidia-smi (telemetria opcional)", report.NvidiaSMIPath, false))
	shmGiB := float64(report.ShmFreeBytes) / (1024 * 1024 * 1024)
	shmOK := report.ShmWritable && report.ShmFreeBytes >= deps.MinShmFreeBytes
	lines = append(lines, formatBoolStatus(fmt.Sprintf("%s (%.2f GiB livres, mín 4.00 GiB)", report.ShmPath, shmGiB), shmOK))
	return strings.Join(lines, "\n")
}

func formatBinaryStatus(name, path string, mandatory bool) string {
	if path != "" {
		return fmt.Sprintf("  %s %-32s %s", okBadgeStyle.Render("✓"), name, path)
	}
	if !mandatory {
		return fmt.Sprintf("  %s %-32s ausente (opcional)", warnBadgeStyle.Render("⚠"), name)
	}
	return fmt.Sprintf("  %s %-32s não encontrado no $PATH", errBadgeStyle.Render("✗"), name)
}

func formatBoolStatus(label string, ok bool) string {
	if ok {
		return fmt.Sprintf("  %s %s", okBadgeStyle.Render("✓"), label)
	}
	return fmt.Sprintf("  %s %s", errBadgeStyle.Render("✗"), label)
}

func renderRemediationSection(missing []deps.MissingDep) string {
	var b strings.Builder
	b.WriteString(errBadgeStyle.Render("✗ Dependências obrigatórias ausentes — Como instalar no Arch / CachyOS:"))
	b.WriteString("\n")
	for _, dep := range missing {
		_, _ = fmt.Fprintf(&b, "\n  • %s: %s\n", dep.Name, dep.Reason)
		_, _ = fmt.Fprintf(&b, "    Comando : %s\n", cmdCodeStyle.Render(dep.InstallCommand))
		_, _ = fmt.Fprintf(&b, "    Fallback: %s\n", dep.FallbackURL)
	}
	return b.String()
}
