package tui_test

import (
	"strings"
	"testing"

	"github.com/gabrigas/aniup/internal/deps"
	"github.com/gabrigas/aniup/internal/tui"
)

func TestRenderDoctorReportIncludesRemediationCommandsWhenMissing(t *testing.T) {
	// Arrange
	report := deps.CheckReport{
		ShmPath:      "/dev/shm",
		ShmWritable:  true,
		ShmFreeBytes: 8 * 1024 * 1024 * 1024,
		MissingMandatory: []deps.MissingDep{
			{
				Name:           "realesrgan-ncnn-vulkan / realcugan-ncnn-vulkan",
				Reason:         "Nenhum motor NCNN Vulkan encontrado no $PATH",
				InstallCommand: "paru -S realesrgan-ncnn-vulkan-bin (ou yay -S realesrgan-ncnn-vulkan-bin)",
				FallbackURL:    "https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan/releases",
			},
		},
	}

	// Act
	rendered := tui.RenderDoctorReport(report)

	// Assert
	if !strings.Contains(rendered, "paru -S realesrgan-ncnn-vulkan-bin") {
		t.Fatalf("expected rendered doctor view to contain install command, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan/releases") {
		t.Fatalf("expected rendered doctor view to contain fallback URL, got:\n%s", rendered)
	}
}

func TestRenderDoctorReportShowsReadyBadgeWhenAllDependenciesExist(t *testing.T) {
	// Arrange
	report := deps.CheckReport{
		FFmpegPath:     "/usr/bin/ffmpeg",
		FFprobePath:    "/usr/bin/ffprobe",
		RealESRGANPath: "/usr/bin/realesrgan-ncnn-vulkan",
		HasHEVCNVENC:   true,
		HasAV1NVENC:    true,
		ShmPath:        "/dev/shm",
		ShmWritable:    true,
		ShmFreeBytes:   8 * 1024 * 1024 * 1024,
	}

	// Act
	rendered := tui.RenderDoctorReport(report)

	// Assert
	if !strings.Contains(rendered, "Ambiente pronto") {
		t.Fatalf("expected ready message, got:\n%s", rendered)
	}
}
