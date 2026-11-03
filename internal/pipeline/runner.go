package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/state"
)

// StageType identifies the active phase of a chunk inside the pipeline.
type StageType string

const (
	StageExtracting    StageType = "Extraindo"
	StageUpscaling     StageType = "Upscaling"
	StageEncoding      StageType = "Codificando NVENC"
	StageConcatenating StageType = "Concatenando"
)

// Event carries real-time pipeline progress updates to the Bubble Tea TUI dashboard.
type Event struct {
	ChunkIndex      int
	TotalChunks     int
	CompletedChunks int
	Stage           StageType
	StagePercent    float64
	FramesDone      int
	TotalFrames     int
	ShmWorkspace    string
	Done            bool
	Err             error
}

// ProcessExecutor abstracts subprocess execution for deterministic unit testing.
type ProcessExecutor interface {
	Run(ctx context.Context, env []string, name string, args ...string) error
	RunWithStderr(ctx context.Context, env []string, onStderrLine func(string), name string, args ...string) error
}

// OSProcessExecutor runs real system commands via exec.CommandContext without shell interpolation.
type OSProcessExecutor struct{}

// NewOSProcessExecutor returns a production ProcessExecutor.
// Example: exec := pipeline.NewOSProcessExecutor()
func NewOSProcessExecutor() *OSProcessExecutor {
	return &OSProcessExecutor{}
}

// Run executes name with explicit args and returns an error including stderr if it fails.
// Example: err := exec.Run(ctx, nil, "ffmpeg", args...)
func (o *OSProcessExecutor) Run(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s failed (%w): %s", name, err, stderr.String())
	}
	return nil
}

// RunWithStderr streams stderr tokens split on '\r' or '\n' via ScanCRLF to onStderrLine in real time.
// Example: err := exec.RunWithStderr(ctx, env, onLine, "realesrgan-ncnn-vulkan", args...)
func (o *OSProcessExecutor) RunWithStderr(
	ctx context.Context,
	env []string,
	onStderrLine func(string),
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to attach stderr pipe for %s: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", name, err)
	}
	scanner := bufio.NewScanner(stderrPipe)
	scanner.Split(ScanCRLF)
	for scanner.Scan() {
		if onStderrLine != nil {
			onStderrLine(scanner.Text())
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

// Runner orchestrates chunked RAM extraction, NCNN upscaling, atomic NVENC encoding, and final concatenation.
type Runner struct {
	exec       ProcessExecutor
	shmBaseDir string
	ffmpegPath string
	enginePath string
}

// NewRunner constructs a pipeline Runner.
// Example: runner := pipeline.NewRunner(exec, "/dev/shm", "ffmpeg", "realesrgan-ncnn-vulkan")
func NewRunner(exec ProcessExecutor, shmBaseDir, ffmpegPath, enginePath string) *Runner {
	if shmBaseDir == "" {
		shmBaseDir = "/dev/shm"
	}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	return &Runner{
		exec:       exec,
		shmBaseDir: shmBaseDir,
		ffmpegPath: ffmpegPath,
		enginePath: enginePath,
	}
}

// Run executes all pending chunks in manifest using an isolated /dev/shm/aniup_<pid> workspace that is always cleaned on exit.
// Example: err := runner.Run(ctx, cfg, workDir, manifest, eventsCh)
func (r *Runner) Run(
	ctx context.Context,
	cfg config.RunConfig,
	workDir string,
	manifest *state.Manifest,
	events chan<- Event,
) (runErr error) {
	shmWorkspace := filepath.Join(r.shmBaseDir, fmt.Sprintf("aniup_%d", os.Getpid()))
	defer func() {
		_ = os.RemoveAll(shmWorkspace)
		if rec := recover(); rec != nil {
			panic(rec)
		}
	}()

	if err := os.MkdirAll(shmWorkspace, 0o700); err != nil {
		return fmt.Errorf("failed to create RAM workspace %q: %w", shmWorkspace, err)
	}

	if err := r.processChunks(ctx, cfg, workDir, shmWorkspace, manifest, events); err != nil {
		emitEvent(events, Event{ShmWorkspace: shmWorkspace, Err: err})
		return err
	}
	return r.finalizeOutput(ctx, cfg, workDir, shmWorkspace, manifest, events)
}

func (r *Runner) processChunks(
	ctx context.Context,
	cfg config.RunConfig,
	workDir, shmWorkspace string,
	manifest *state.Manifest,
	events chan<- Event,
) error {
	totalChunks := len(manifest.Chunks)
	current := manifest
	for i, chunk := range current.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		segPath := filepath.Join(workDir, chunk.SegmentFile)
		if chunk.Status == state.ChunkCompleted && fileNonEmpty(segPath) {
			continue
		}
		updated, err := r.runSingleChunk(ctx, cfg, workDir, shmWorkspace, current, i, totalChunks, events)
		if err != nil {
			return err
		}
		current = updated
	}
	return nil
}

func (r *Runner) runSingleChunk(
	ctx context.Context,
	cfg config.RunConfig,
	workDir, shmWorkspace string,
	manifest *state.Manifest,
	idx, totalChunks int,
	events chan<- Event,
) (*state.Manifest, error) {
	inDir := filepath.Join(shmWorkspace, "in")
	outDir := filepath.Join(shmWorkspace, "out")
	if err := resetChunkDirs(inDir, outDir); err != nil {
		return nil, err
	}
	defer func() {
		_ = os.RemoveAll(inDir)
		_ = os.RemoveAll(outDir)
	}()

	chunk := manifest.Chunks[idx]
	completed := manifest.CompletedCount()
	if err := r.extractAndUpscaleChunk(ctx, cfg, shmWorkspace, inDir, outDir, manifest, idx, totalChunks, completed, events); err != nil {
		return nil, err
	}
	emitChunkStage(events, idx, totalChunks, completed, StageEncoding, 100, chunk.EndFrame+1, manifest.TotalFrames, shmWorkspace)
	segHash, err := EncodeChunkAtomic(ctx, r.exec, r.ffmpegPath, outDir, manifest.FrameRate, cfg.Encoder, workDir, chunk.SegmentFile)
	if err != nil {
		return nil, err
	}
	return state.MarkChunkCompleted(workDir, manifest, idx, chunk.SegmentFile, segHash)
}

func (r *Runner) extractAndUpscaleChunk(
	ctx context.Context,
	cfg config.RunConfig,
	shmWorkspace, inDir, outDir string,
	manifest *state.Manifest,
	idx, totalChunks, completed int,
	events chan<- Event,
) error {
	chunk := manifest.Chunks[idx]
	framesDone := completed * cfg.ChunkSize
	emitChunkStage(events, idx, totalChunks, completed, StageExtracting, 0, framesDone, manifest.TotalFrames, shmWorkspace)
	if err := ExtractChunk(ctx, r.exec, r.ffmpegPath, cfg.InputPath, inDir, chunk.StartFrame, chunk.EndFrame); err != nil {
		return err
	}
	emitChunkStage(events, idx, totalChunks, completed, StageUpscaling, 0, framesDone, manifest.TotalFrames, shmWorkspace)
	return UpscaleChunk(ctx, r.exec, r.enginePath, cfg, inDir, outDir, func(pct float64) {
		chunkFrames := int((pct / 100.0) * float64(chunk.EndFrame-chunk.StartFrame+1))
		emitChunkStage(events, idx, totalChunks, completed, StageUpscaling, pct, framesDone+chunkFrames, manifest.TotalFrames, shmWorkspace)
	})
}

func emitChunkStage(
	events chan<- Event,
	idx, totalChunks, completed int,
	stage StageType,
	pct float64,
	framesDone, totalFrames int,
	shmWorkspace string,
) {
	emitEvent(events, Event{
		ChunkIndex: idx, TotalChunks: totalChunks, CompletedChunks: completed,
		Stage: stage, StagePercent: pct, FramesDone: framesDone,
		TotalFrames: totalFrames, ShmWorkspace: shmWorkspace,
	})
}

func (r *Runner) finalizeOutput(
	ctx context.Context,
	cfg config.RunConfig,
	workDir, shmWorkspace string,
	manifest *state.Manifest,
	events chan<- Event,
) error {
	totalChunks := len(manifest.Chunks)
	emitEvent(events, Event{
		ChunkIndex: totalChunks - 1, TotalChunks: totalChunks, CompletedChunks: totalChunks,
		Stage: StageConcatenating, StagePercent: 100, FramesDone: manifest.TotalFrames,
		TotalFrames: manifest.TotalFrames, ShmWorkspace: shmWorkspace,
	})
	if err := ConcatFinalVideo(ctx, r.exec, r.ffmpegPath, workDir, manifest.Chunks, cfg.InputPath, cfg.OutputPath); err != nil {
		emitEvent(events, Event{ShmWorkspace: shmWorkspace, Err: err})
		return err
	}
	if !fileNonEmpty(cfg.OutputPath) {
		err := fmt.Errorf("final output file %q is missing or empty after concatenation", cfg.OutputPath)
		emitEvent(events, Event{ShmWorkspace: shmWorkspace, Err: err})
		return err
	}
	if !cfg.KeepChunks {
		_ = os.RemoveAll(workDir)
	}
	emitEvent(events, Event{
		ChunkIndex: totalChunks - 1, TotalChunks: totalChunks, CompletedChunks: totalChunks,
		Stage: StageConcatenating, StagePercent: 100, FramesDone: manifest.TotalFrames,
		TotalFrames: manifest.TotalFrames, ShmWorkspace: shmWorkspace, Done: true,
	})
	return nil
}

func resetChunkDirs(inDir, outDir string) error {
	_ = os.RemoveAll(inDir)
	_ = os.RemoveAll(outDir)
	if err := os.MkdirAll(inDir, 0o700); err != nil {
		return fmt.Errorf("failed to create chunk input dir %q: %w", inDir, err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("failed to create chunk output dir %q: %w", outDir, err)
	}
	return nil
}

func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func emitEvent(ch chan<- Event, ev Event) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
}
