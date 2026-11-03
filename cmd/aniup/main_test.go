package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/deps"
	"github.com/gabrigas/aniup/internal/state"
)

// FakeDoctorInspector is a named fake implementation of deps.SystemInspector for CLI doctor tests.
type FakeDoctorInspector struct {
	AllPresent bool
}

func (f *FakeDoctorInspector) LookPath(file string) (string, error) {
	if f.AllPresent {
		return "/usr/bin/" + file, nil
	}
	return "", nil
}

func (f *FakeDoctorInspector) RunCommand(_ context.Context, _ string, _ ...string) (string, error) {
	if f.AllPresent {
		return "hevc_nvenc av1_nvenc", nil
	}
	return "", nil
}

func (f *FakeDoctorInspector) ShmStatus(_ string) (uint64, bool, error) {
	if f.AllPresent {
		return 8 * 1024 * 1024 * 1024, true, nil
	}
	return 1024, false, nil
}

// FakeProbeAndDepsInspector returns valid ffprobe JSON for resolveConfigAndProbe tests.
type FakeProbeAndDepsInspector struct {
	FakeDoctorInspector
}

func (f *FakeProbeAndDepsInspector) RunCommand(_ context.Context, name string, _ ...string) (string, error) {
	if strings.Contains(name, "ffprobe") {
		return `{"streams":[{"codec_type":"video","width":1920,"height":1080,"r_frame_rate":"24/1","nb_frames":"120"}],"format":{"duration":"5.0"}}`, nil
	}
	return "hevc_nvenc av1_nvenc", nil
}

// FakeCLIProcessExecutor simulates ffmpeg and ncnn execution for end-to-end CLI tests.
type FakeCLIProcessExecutor struct{}

func (f *FakeCLIProcessExecutor) Run(ctx context.Context, _ []string, _ string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) > 0 {
		last := args[len(args)-1]
		if strings.HasSuffix(last, ".mkv") {
			_ = os.MkdirAll(filepath.Dir(last), 0o755)
			_ = os.WriteFile(last, []byte("encoded-mkv"), 0o600)
		}
	}
	return nil
}

func (f *FakeCLIProcessExecutor) RunWithStderr(ctx context.Context, _ []string, onLine func(string), _ string, _ ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if onLine != nil {
		onLine("100.00%")
	}
	return nil
}

func TestParseCLIArgsHandlesDoctorAndFlags(t *testing.T) {
	// Arrange & Act: --help and -h flags
	for _, helpArg := range []string{"--help", "-h", "help"} {
		flags, isDoctor, err := ParseCLIArgs([]string{helpArg})
		if err != nil || isDoctor || !flags.ShowHelp {
			t.Fatalf("expected %q to set ShowHelp=true without error, got flags=%+v isDoctor=%v err=%v", helpArg, flags, isDoctor, err)
		}
	}

	// Arrange & Act: doctor subcommand
	flags, isDoctor, err := ParseCLIArgs([]string{"doctor"})
	if err != nil || !isDoctor {
		t.Fatalf("expected doctor subcommand, got isDoctor=%v err=%v", isDoctor, err)
	}

	// Arrange & Act: positional file only -> needs wizard
	flags, isDoctor, err = ParseCLIArgs([]string{"episode01.mkv"})
	if err != nil || isDoctor || flags.InputPath != "episode01.mkv" || !NeedsWizard(flags) {
		t.Fatalf("expected positional input requiring wizard, got flags=%+v isDoctor=%v err=%v", flags, isDoctor, err)
	}

	// Arrange & Act: full flags -> does not need wizard
	fullArgs := []string{
		"-i", "episode01.mkv",
		"--engine", string(config.EngineRealESRGAN),
		"--model", "realesr-animevideov3",
		"--scale", "2",
		"--profile", "turbo",
		"--chunk-size", "600",
		"--encoder", "hevc_nvenc",
		"--keep-chunks",
	}
	flags, isDoctor, err = ParseCLIArgs(fullArgs)
	if err != nil || isDoctor || NeedsWizard(flags) || !flags.KeepChunks {
		t.Fatalf("expected full CLI flags not to need wizard, got flags=%+v err=%v", flags, err)
	}
}

func TestRunDoctorAndRunAppEndToEnd(t *testing.T) {
	// Arrange --help via runApp -> exit code 0 with usage output
	var out bytes.Buffer
	codeHelp := runApp(context.Background(), func() {}, []string{"--help"}, &FakeDoctorInspector{AllPresent: true}, &FakeCLIProcessExecutor{}, &out, true)
	if codeHelp != 0 || !strings.Contains(out.String(), "--engine") || !strings.Contains(out.String(), "doctor") {
		t.Fatalf("expected exit code 0 and help text for --help, got code=%d out:\n%s", codeHelp, out.String())
	}

	// Arrange healthy doctor
	out.Reset()
	codeOK := runApp(context.Background(), func() {}, []string{"doctor"}, &FakeDoctorInspector{AllPresent: true}, &FakeCLIProcessExecutor{}, &out, true)
	if codeOK != 0 || !strings.Contains(out.String(), "Ambiente pronto") {
		t.Fatalf("expected exit code 0, got %d with output:\n%s", codeOK, out.String())
	}

	// Arrange invalid flag -> exit code 2
	out.Reset()
	codeBadFlag := runApp(context.Background(), func() {}, []string{"--unknown-flag"}, &FakeDoctorInspector{AllPresent: true}, &FakeCLIProcessExecutor{}, &out, true)
	if codeBadFlag != 2 {
		t.Fatalf("expected exit code 2 on bad flag, got %d", codeBadFlag)
	}

	// Arrange missing deps -> exit code 1
	out.Reset()
	codeFail := RunDoctor(context.Background(), &FakeDoctorInspector{AllPresent: false}, &out)
	if codeFail != 1 || !strings.Contains(out.String(), "pacman -S ffmpeg") {
		t.Fatalf("expected exit code 1 with install instructions, got %d:\n%s", codeFail, out.String())
	}

	// Arrange end-to-end headless pipeline run via runApp
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "ep01.mkv")
	outputPath := filepath.Join(tmpDir, "ep01_out.mkv")
	if err := os.WriteFile(inputPath, []byte("video-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := &FakeProbeAndDepsInspector{FakeDoctorInspector{AllPresent: true}}
	fullArgs := []string{
		"-i", inputPath,
		"-o", outputPath,
		"--engine", string(config.EngineRealCUGAN),
		"--model", "models-se",
		"--scale", "2",
		"--profile", "turbo",
		"--chunk-size", "600",
		"--encoder", "hevc_nvenc",
	}
	codeRun := runApp(context.Background(), func() {}, fullArgs, inspector, &FakeCLIProcessExecutor{}, &out, true)
	if codeRun != 0 {
		t.Fatalf("expected exit code 0 for headless runApp, got %d (%s)", codeRun, out.String())
	}

	// Arrange canceled context (user pressed 'q') -> must return exit code 0 without "Erro:"
	out.Reset()
	canceledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	codePaused := runApp(canceledCtx, func() {}, fullArgs, inspector, &FakeCLIProcessExecutor{}, &out, true)
	if codePaused != 0 || strings.Contains(out.String(), "Erro:") {
		t.Fatalf("expected exit code 0 on user pause (context.Canceled), got code=%d out=%q", codePaused, out.String())
	}
}

func TestResolveConfigAndPrepareCheckpointManifest(t *testing.T) {
	// Arrange
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "ep01.mkv")
	if err := os.WriteFile(inputPath, []byte("video-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspector := &FakeProbeAndDepsInspector{FakeDoctorInspector{AllPresent: true}}
	report := deps.CheckAll(context.Background(), inspector, tmpDir)
	flags := CLIFlags{
		InputPath: inputPath,
		Engine:    string(config.EngineRealESRGAN),
		Model:     "realesr-animevideov3",
		Scale:     2,
		Profile:   string(config.ProfileTurbo),
		ChunkSize: 600,
		Encoder:   string(config.EncoderHEVCNVENC),
		Resume:    true,
	}

	// Act: resolveConfigAndProbe
	cfg, meta, err := resolveConfigAndProbe(context.Background(), inspector, report, flags)
	if err != nil {
		t.Fatalf("unexpected resolveConfigAndProbe error: %v", err)
	}

	// Act: prepare fresh manifest
	workDir, m1, err := prepareCheckpointManifest(cfg, meta, false)
	if err != nil || len(m1.Chunks) != 1 {
		t.Fatalf("unexpected prepareCheckpointManifest result: len=%d err=%v", len(m1.Chunks), err)
	}

	// Mark chunk 0 completed and test resume path
	if _, err := state.MarkChunkCompleted(workDir, m1, 0, "seg_00001.mkv", "sha1"); err != nil {
		t.Fatal(err)
	}
	_, m2, err := prepareCheckpointManifest(cfg, meta, false)
	if err != nil || m2.CompletedCount() != 1 {
		t.Fatalf("expected resumed manifest with 1 completed chunk, got count=%d err=%v", m2.CompletedCount(), err)
	}

	// Test resume with ONLY input path + --resume (no other flags!) loads config from manifest without Wizard
	resumeOnlyFlags := CLIFlags{
		InputPath: inputPath,
		Resume:    true,
	}
	resumedCfg, _, err := resolveConfigAndProbe(context.Background(), inspector, report, resumeOnlyFlags)
	if err != nil || resumedCfg.Model != "realesr-animevideov3" || resumedCfg.Scale != 2 {
		t.Fatalf("expected resolveConfigAndProbe with --resume to restore config from manifest, got cfg=%+v err=%v", resumedCfg, err)
	}

	// Test non-resume overwrites with fresh manifest (0 completed)
	cfgNoResume := cfg
	cfgNoResume.Resume = false
	_, mFresh, err := prepareCheckpointManifest(cfgNoResume, meta, false)
	if err != nil || mFresh.CompletedCount() != 0 {
		t.Fatalf("expected fresh manifest with 0 completed chunks when Resume=false, got count=%d err=%v", mFresh.CompletedCount(), err)
	}

	// Test resolveConfigAndProbe rejects invalid scale
	badFlags := flags
	badFlags.Scale = 99
	if _, _, err := resolveConfigAndProbe(context.Background(), inspector, report, badFlags); err == nil {
		t.Fatal("expected resolveConfigAndProbe to reject invalid scale")
	}

	// Test executeMainFlow aborts when dependencies are missing
	var out bytes.Buffer
	if err := executeMainFlow(context.Background(), func() {}, &FakeDoctorInspector{AllPresent: false}, &FakeCLIProcessExecutor{}, flags, &out, true); err == nil {
		t.Fatal("expected executeMainFlow to fail when dependencies are missing")
	}
}
