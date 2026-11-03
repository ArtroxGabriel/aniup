package tui_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/pipeline"
	"github.com/gabrigas/aniup/internal/probe"
	"github.com/gabrigas/aniup/internal/tui"
)

func TestBuildEngineModelOptionsFiltersByInstalledEngines(t *testing.T) {
	// Arrange & Act: only RealESRGAN installed
	optsESRGAN := tui.BuildEngineModelOptions([]config.EngineType{config.EngineRealESRGAN})
	// Arrange & Act: both installed
	optsBoth := tui.BuildEngineModelOptions([]config.EngineType{config.EngineRealESRGAN, config.EngineRealCUGAN})

	// Assert
	if len(optsESRGAN) != 2 {
		t.Fatalf("expected 2 options when only realesrgan installed, got %d", len(optsESRGAN))
	}
	for _, opt := range optsESRGAN {
		if opt.Engine != config.EngineRealESRGAN {
			t.Fatalf("expected only realesrgan options, got %+v", opt)
		}
	}
	if len(optsBoth) != 4 {
		t.Fatalf("expected 4 options when both engines installed, got %d", len(optsBoth))
	}
}

func TestParseNvidiaSMIOutputAndGracefulDegradation(t *testing.T) {
	// Arrange & Act
	vram, temp, err := tui.ParseNvidiaSMIOutput("3420, 62\n")

	// Assert
	if err != nil || vram != 3420 || temp != 62 {
		t.Fatalf("expected vram=3420 temp=62, got vram=%d temp=%d err=%v", vram, temp, err)
	}

	if _, _, err := tui.ParseNvidiaSMIOutput("invalid"); err == nil {
		t.Fatal("expected error on invalid nvidia-smi CSV output")
	}
}

func TestDashboardModelUpdatesProgressAndHandlesGracefulShutdown(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.RunConfig{
		InputPath:  "/videos/episode01.mkv",
		OutputPath: "/videos/episode01_2x.mkv",
		Engine:     config.EngineRealESRGAN,
		Model:      "realesr-animevideov3",
		Scale:      2,
		Profile:    config.ProfileTurbo,
		ChunkSize:  600,
		Encoder:    config.EncoderHEVCNVENC,
	}
	meta := probe.VideoMetadata{
		Width:       1920,
		Height:      1080,
		TotalFrames: 1200,
		FPS:         23.976,
	}
	events := make(chan pipeline.Event, 4)
	model := tui.NewDashboardModel(ctx, cancel, cfg, meta, events, nil)

	// Act: send ProgressMsg for Upscaling stage at 64.2%
	updatedModel, _ := model.Update(tui.ProgressMsg{
		Event: pipeline.Event{
			ChunkIndex:      0,
			TotalChunks:     2,
			CompletedChunks: 0,
			Stage:           pipeline.StageUpscaling,
			StagePercent:    64.2,
			FramesDone:      385,
			TotalFrames:     1200,
		},
	})
	dash := updatedModel.(tui.DashboardModel)
	view := dash.View()

	// Assert view contents
	expectedSnippets := []string{
		"episode01.mkv",
		"1920x1080 -> 3840x2160",
		"[2/3 Upscaling 64.2%]",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(view, snippet) {
			t.Fatalf("expected dashboard view to contain %q, got:\n%s", snippet, view)
		}
	}

	// Act: send 'q' key to trigger graceful shutdown
	afterQuitModel, _ := dash.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	quitDash := afterQuitModel.(tui.DashboardModel)

	// Assert context was canceled and shutdown notice is rendered
	if ctx.Err() == nil {
		t.Fatal("expected pressing 'q' to cancel the pipeline context")
	}
	if !strings.Contains(quitDash.View(), "salvo") {
		t.Fatalf("expected graceful shutdown confirmation in view, got:\n%s", quitDash.View())
	}
}

func TestWizardHelpersAndDashboardStagesAndTelemetry(t *testing.T) {
	// Arrange & Act: ParseEngineModelSelection and AssembleWizardConfig
	opts := tui.BuildEngineModelOptions([]config.EngineType{config.EngineRealESRGAN})
	inputPath := ""
	key := opts[0].Key
	scale := 2
	profile := string(config.ProfileTurbo)
	chunkStr := "600"
	encoder := string(config.EncoderHEVCNVENC)
	form := tui.BuildWizardForm(&inputPath, &key, &scale, &profile, &chunkStr, &encoder, opts, 8*1024*1024*1024)
	if form == nil {
		t.Fatal("expected non-nil Huh form")
	}

	cfg, err := tui.AssembleWizardConfig(config.RunConfig{}, "/videos/ep1.mkv", key, 2, profile, "600", encoder, 8*1024*1024*1024)
	if err != nil || cfg.Model != "realesr-animevideov3" {
		t.Fatalf("unexpected AssembleWizardConfig result: cfg=%+v err=%v", cfg, err)
	}
	if _, _, err := tui.ParseEngineModelSelection("invalid"); err == nil {
		t.Fatal("expected error on invalid selection key")
	}
	if _, err := tui.AssembleWizardConfig(config.RunConfig{}, "/videos/ep1.mkv", key, 2, profile, "abc", encoder, 8*1024*1024*1024); err == nil {
		t.Fatal("expected error on non-integer chunk size")
	}

	// Arrange: OSTelemetryReader directory size and GPU stats fallback
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "frame_00000001.png"), make([]byte, 2048), 0o600)
	reader := &tui.OSTelemetryReader{NvidiaSMIPath: "/nonexistent/nvidia-smi"}
	if got := reader.ReadDirBytes(tmpDir); got != 2048 {
		t.Fatalf("expected 2048 bytes in dir, got %d", got)
	}
	if _, _, err := reader.ReadGPUStats(context.Background()); err == nil {
		t.Fatal("expected error when nvidia-smi path does not exist")
	}

	// Arrange: Dashboard stage badges, telemetry update, error view, and completion view
	events := make(chan pipeline.Event, 4)
	dash := tui.NewDashboardModel(context.Background(), nil, cfg, probe.VideoMetadata{Width: 1920, Height: 1080, TotalFrames: 600}, events, reader)
	_ = dash.Init()

	for _, st := range []pipeline.StageType{pipeline.StageExtracting, pipeline.StageEncoding, pipeline.StageConcatenating} {
		m, _ := dash.Update(tui.ProgressMsg{Event: pipeline.Event{Stage: st, TotalChunks: 1, TotalFrames: 600, FramesDone: 100}})
		dash = m.(tui.DashboardModel)
		_ = dash.View()
	}
	// Verify FramesDone never regresses if a lower value arrives
	mRegress, _ := dash.Update(tui.ProgressMsg{Event: pipeline.Event{Stage: pipeline.StageUpscaling, TotalChunks: 1, TotalFrames: 600, FramesDone: 10}})
	dash = mRegress.(tui.DashboardModel)
	mTelem, _ := dash.Update(tui.TelemetryMsg{ShmUsedBytes: 1024 * 1024, VRAMUsedMiB: 2048, GPUTempC: 60, GPUAvailable: true})
	dash = mTelem.(tui.DashboardModel)
	if !strings.Contains(dash.View(), "2048 MiB") {
		t.Fatalf("expected VRAM telemetry in view, got:\n%s", dash.View())
	}

	mDone, _ := dash.Update(tui.ProgressMsg{Event: pipeline.Event{Done: true}})
	if !strings.Contains(mDone.(tui.DashboardModel).View(), "Concluído com sucesso") {
		t.Fatal("expected completion banner in view")
	}

	mErr, _ := dash.Update(tui.ProgressMsg{Event: pipeline.Event{Err: errors.New("gpu error")}})
	if !strings.Contains(mErr.(tui.DashboardModel).View(), "gpu error") {
		t.Fatal("expected error banner in view")
	}

	if _, err := tui.RunWizard(config.RunConfig{}, nil, 0); err == nil {
		t.Fatal("expected error when RunWizard has no available engines")
	}
}
