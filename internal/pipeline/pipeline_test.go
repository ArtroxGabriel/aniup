package pipeline_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/pipeline"
	"github.com/gabrigas/aniup/internal/state"
)

// FakeProcessExecutor is a named fake implementation of pipeline.ProcessExecutor for deterministic tests.
type FakeProcessExecutor struct {
	RecordedCmds [][]string
	RecordedEnvs [][]string
	StderrLines  []string
	FailOnBin    string
	CancelDuring string
	CancelFunc   context.CancelFunc
}

func (f *FakeProcessExecutor) Run(ctx context.Context, env []string, name string, args ...string) error {
	f.RecordedCmds = append(f.RecordedCmds, append([]string{name}, args...))
	f.RecordedEnvs = append(f.RecordedEnvs, append([]string(nil), env...))
	if f.FailOnBin != "" && strings.Contains(name, f.FailOnBin) {
		return errors.New("simulated process failure")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// If this is an ffmpeg segment or final output command, create the output file (last arg) with non-empty bytes.
	if len(args) > 0 {
		outPath := args[len(args)-1]
		if strings.HasSuffix(outPath, ".mkv") {
			_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
			_ = os.WriteFile(outPath, []byte("fake-mkv-stream-bytes"), 0o600)
		}
	}
	return nil
}

func (f *FakeProcessExecutor) RunWithStderr(ctx context.Context, env []string, onLine func(string), name string, args ...string) error {
	f.RecordedCmds = append(f.RecordedCmds, append([]string{name}, args...))
	f.RecordedEnvs = append(f.RecordedEnvs, append([]string(nil), env...))
	for _, line := range f.StderrLines {
		if onLine != nil {
			onLine(line)
		}
	}
	if f.CancelDuring != "" && strings.Contains(name, f.CancelDuring) && f.CancelFunc != nil {
		f.CancelFunc()
		return context.Canceled
	}
	if f.FailOnBin != "" && strings.Contains(name, f.FailOnBin) {
		return errors.New("simulated upscaler failure")
	}
	return ctx.Err()
}

func TestScanCRLFSplitsOnCarriageReturnAndNewline(t *testing.T) {
	// Arrange
	raw := "0.00%\r25.50%\r\n64.20%\n100.00%"
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Split(pipeline.ScanCRLF)

	// Act
	var tokens []string
	for scanner.Scan() {
		tokens = append(tokens, scanner.Text())
	}

	// Assert
	expected := []string{"0.00%", "25.50%", "64.20%", "100.00%"}
	if len(tokens) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, tokens)
	}
	for i := range expected {
		if tokens[i] != expected[i] {
			t.Fatalf("token[%d]: expected %q, got %q", i, expected[i], tokens[i])
		}
	}
}

func TestParseProgressPercentExtractsFloatPercentage(t *testing.T) {
	// Arrange & Act & Assert
	pct, ok := pipeline.ParseProgressPercent("[0 NVIDIA GeForce RTX 4050] 64.25% ")
	if !ok || pct != 64.25 {
		t.Fatalf("expected 64.25%%, got pct=%v ok=%v", pct, ok)
	}

	if _, ok := pipeline.ParseProgressPercent("vkCreateInstance succeeded"); ok {
		t.Fatal("expected non-progress line to return ok=false")
	}
}

func TestBuildUpscaleAndConcatArgsMatchSpec(t *testing.T) {
	// Arrange
	esrganCfg := config.RunConfig{
		Engine:  config.EngineRealESRGAN,
		Model:   "realesr-animevideov3",
		Scale:   2,
		Profile: config.ProfileTurbo,
	}
	cuganCfg := config.RunConfig{
		Engine:  config.EngineRealCUGAN,
		Model:   "models-se",
		Scale:   2,
		Profile: config.ProfileBalanced,
	}

	// Act
	esrganArgs := pipeline.BuildUpscaleArgs(esrganCfg, "/dev/shm/in", "/dev/shm/out")
	cuganArgs := pipeline.BuildUpscaleArgs(cuganCfg, "/dev/shm/in", "/dev/shm/out")
	concatArgs := pipeline.BuildFinalConcatArgs("/work/concat.txt", "/videos/input.mkv", "/videos/output.mkv")

	// Assert
	if !strings.Contains(strings.Join(esrganArgs, " "), "-n realesr-animevideov3 -s 2 -t 768 -j 2:3:3 -f png") {
		t.Fatalf("unexpected realesrgan args: %v", esrganArgs)
	}
	if !strings.Contains(strings.Join(cuganArgs, " "), "-m models-se -s 2 -t 400 -j 1:2:2 -f png") {
		t.Fatalf("unexpected realcugan args: %v", cuganArgs)
	}
	expectedConcat := "-f concat -safe 0 -i /work/concat.txt -i /videos/input.mkv -map 0:v:0 -map 1:a? -map 1:s? -map_chapters 1 -c copy /videos/output.mkv"
	if !strings.Contains(strings.Join(concatArgs, " "), expectedConcat) {
		t.Fatalf("unexpected concat args: %v", concatArgs)
	}
}

func TestRunnerExecutesChunksAtomicallyAndCleansShm(t *testing.T) {
	// Arrange
	tmpRoot := t.TempDir()
	shmBase := filepath.Join(tmpRoot, "shm")
	if err := os.MkdirAll(shmBase, 0o755); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(tmpRoot, "episode.mkv")
	outputPath := filepath.Join(tmpRoot, "episode_2x.mkv")
	if err := os.WriteFile(inputPath, []byte("source-video"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.RunConfig{
		InputPath:  inputPath,
		OutputPath: outputPath,
		Engine:     config.EngineRealESRGAN,
		Model:      "realesr-animevideov3",
		Scale:      2,
		Profile:    config.ProfileTurbo,
		ChunkSize:  100,
		Encoder:    config.EncoderHEVCNVENC,
		KeepChunks: false,
	}
	workDir := state.WorkDirPath(inputPath, cfg.Scale)
	manifest := state.NewManifest(cfg, "hash123", 150, "24000/1001") // 2 chunks
	if err := state.SaveManifest(workDir, manifest); err != nil {
		t.Fatal(err)
	}

	fakeExec := &FakeProcessExecutor{
		StderrLines: []string{"12.50%", "50.00%", "100.00%"},
	}
	events := make(chan pipeline.Event, 64)
	runner := pipeline.NewRunner(fakeExec, shmBase, "ffmpeg", string(cfg.Engine))

	// Act
	err := runner.Run(context.Background(), cfg, workDir, manifest, events)
	close(events)

	// Assert
	if err != nil {
		t.Fatalf("expected runner to succeed, got: %v", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty final output video, got info=%v err=%v", info, err)
	}
	// Verify /dev/shm workspace was completely cleaned up
	entries, err := os.ReadDir(shmBase)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected zero orphan dirs in shmBase, found %d (err=%v)", len(entries), err)
	}
	// Verify workDir was removed since KeepChunks == false
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("expected workDir %q to be removed after successful concat", workDir)
	}
}

func TestRunnerCleansShmAndPreservesManifestOnCancellation(t *testing.T) {
	// Arrange
	tmpRoot := t.TempDir()
	shmBase := filepath.Join(tmpRoot, "shm")
	_ = os.MkdirAll(shmBase, 0o755)
	inputPath := filepath.Join(tmpRoot, "episode.mkv")
	_ = os.WriteFile(inputPath, []byte("source-video"), 0o600)

	cfg := config.RunConfig{
		InputPath:  inputPath,
		OutputPath: filepath.Join(tmpRoot, "out.mkv"),
		Engine:     config.EngineRealESRGAN,
		Model:      "realesr-animevideov3",
		Scale:      2,
		Profile:    config.ProfileTurbo,
		ChunkSize:  100,
		Encoder:    config.EncoderHEVCNVENC,
	}
	workDir := state.WorkDirPath(inputPath, cfg.Scale)
	manifest := state.NewManifest(cfg, "hash123", 100, "24/1")
	_ = state.SaveManifest(workDir, manifest)

	ctx, cancel := context.WithCancel(context.Background())
	fakeExec := &FakeProcessExecutor{
		CancelDuring: "realesrgan",
		CancelFunc:   cancel,
	}
	runner := pipeline.NewRunner(fakeExec, shmBase, "ffmpeg", "realesrgan-ncnn-vulkan")

	// Act
	err := runner.Run(ctx, cfg, workDir, manifest, nil)

	// Assert
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
	entries, _ := os.ReadDir(shmBase)
	if len(entries) != 0 {
		t.Fatalf("expected zero orphan dirs in shmBase after cancel, found %d", len(entries))
	}
	if _, statErr := os.Stat(filepath.Join(workDir, state.ManifestFileName)); statErr != nil {
		t.Fatalf("expected checkpoint manifest to remain intact on disk: %v", statErr)
	}
}

func TestOSProcessExecutorAndResumeSkipsCompletedChunks(t *testing.T) {
	// Arrange: test OSProcessExecutor with standard POSIX binaries
	osExec := pipeline.NewOSProcessExecutor()
	var lines []string
	err := osExec.RunWithStderr(context.Background(), pipeline.PrimeOffloadEnv(nil), func(line string) {
		lines = append(lines, line)
	}, "printf", "25.00%%\r75.00%%\n")
	if err != nil {
		t.Fatalf("unexpected RunWithStderr error: %v", err)
	}
	if err := osExec.Run(context.Background(), nil, "true"); err != nil {
		t.Fatalf("unexpected Run(true) error: %v", err)
	}
	if err := osExec.Run(context.Background(), nil, "false"); err == nil {
		t.Fatal("expected error from Run(false)")
	}

	// Arrange: resume scenario where chunk 0 is already completed on disk and KeepChunks=true
	tmpRoot := t.TempDir()
	shmBase := filepath.Join(tmpRoot, "shm")
	_ = os.MkdirAll(shmBase, 0o755)
	inputPath := filepath.Join(tmpRoot, "ep.mkv")
	outputPath := filepath.Join(tmpRoot, "ep_out.mkv")
	_ = os.WriteFile(inputPath, []byte("video"), 0o600)

	cfg := config.RunConfig{
		InputPath:  inputPath,
		OutputPath: outputPath,
		Engine:     config.EngineRealESRGAN,
		Model:      "realesr-animevideov3",
		Scale:      2,
		Profile:    config.ProfileTurbo,
		ChunkSize:  100,
		Encoder:    config.EncoderHEVCNVENC,
		KeepChunks: true,
	}
	workDir := state.WorkDirPath(inputPath, cfg.Scale)
	manifest := state.NewManifest(cfg, "hash", 200, "24/1")
	_ = state.SaveManifest(workDir, manifest)
	seg1Path := filepath.Join(workDir, "seg_00001.mkv")
	_ = os.WriteFile(seg1Path, []byte("completed-seg-1"), 0o600)
	manifest, _ = state.MarkChunkCompleted(workDir, manifest, 0, "seg_00001.mkv", "sha1")

	fakeExec := &FakeProcessExecutor{StderrLines: []string{"100.00%"}}
	runner := pipeline.NewRunner(fakeExec, shmBase, "", "realesrgan-ncnn-vulkan")

	// Act
	if err := runner.Run(context.Background(), cfg, workDir, manifest, nil); err != nil {
		t.Fatalf("unexpected resume run error: %v", err)
	}

	// Assert: only chunk 1 was extracted/upscaled/encoded + 1 final concat = 4 commands total (not 7)
	if len(fakeExec.RecordedCmds) != 4 {
		t.Fatalf("expected 4 commands when chunk 0 is skipped, got %d", len(fakeExec.RecordedCmds))
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Fatalf("expected workDir to be preserved when KeepChunks=true: %v", err)
	}
}

// FakePerFrameNCNNExecutor simulates NCNN printing 0.00% -> 50.00% per frame while writing PNGs to outDir.
type FakePerFrameNCNNExecutor struct {
	OutDir string
	Frames int
}

func (f *FakePerFrameNCNNExecutor) Run(_ context.Context, _ []string, _ string, _ ...string) error {
	return nil
}

func (f *FakePerFrameNCNNExecutor) RunWithStderr(_ context.Context, _ []string, onLine func(string), _ string, _ ...string) error {
	for i := 1; i <= f.Frames; i++ {
		onLine("0.00%")
		onLine("50.00%")
		framePath := filepath.Join(f.OutDir, filepath.Base(filepath.Join("out", "frame_0000000"+string(rune('0'+i))+".png")))
		_ = os.WriteFile(framePath, []byte("png"), 0o600)
		onLine("0.00%")
	}
	return nil
}

func TestUpscaleChunkComputesMonotonicProgressAcrossDirectoryFrames(t *testing.T) {
	// Arrange: 4 input frames in inDir
	tmpDir := t.TempDir()
	inDir := filepath.Join(tmpDir, "in")
	outDir := filepath.Join(tmpDir, "out")
	_ = os.MkdirAll(inDir, 0o700)
	_ = os.MkdirAll(outDir, 0o700)
	for i := 1; i <= 4; i++ {
		name := filepath.Join(inDir, "frame_0000000"+string(rune('0'+i))+".png")
		if err := os.WriteFile(name, []byte("in-png"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.RunConfig{
		Engine:  config.EngineRealESRGAN,
		Model:   "realesr-animevideov3",
		Scale:   2,
		Profile: config.ProfileTurbo,
	}
	fakeNCNN := &FakePerFrameNCNNExecutor{OutDir: outDir, Frames: 4}

	// Act
	var history []float64
	err := pipeline.UpscaleChunk(context.Background(), fakeNCNN, "realesrgan-ncnn-vulkan", cfg, inDir, outDir, func(pct float64) {
		history = append(history, pct)
	})
	if err != nil {
		t.Fatalf("unexpected UpscaleChunk error: %v", err)
	}

	// Assert: progress must be strictly non-decreasing (never blinking 50% -> 0%)
	if len(history) == 0 {
		t.Fatal("expected progress callbacks to be emitted")
	}
	for i := 1; i < len(history); i++ {
		if history[i] < history[i-1] {
			t.Fatalf("progress went backward (blinked) at index %d: %v", i, history)
		}
	}
	// On frame 1 tile 2 (50% of 1 frame out of 4), progress should be ~12.5%, NOT 50% of the whole chunk
	if history[0] > 20.0 {
		t.Fatalf("expected first frame tile progress <= 20%%, got %.2f%% (history=%v)", history[0], history)
	}
}
