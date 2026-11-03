package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabrigas/aniup/internal/config"
)

const (
	PrimeOffloadEnvVar = "__NV_PRIME_RENDER_OFFLOAD=1"
	GlxVendorNvidiaVar = "__GLX_VENDOR_LIBRARY_NAME=nvidia"
	MaxProgressPercent = 100.0
	OutDirPollInterval = 200 * time.Millisecond
)

var progressRegex = regexp.MustCompile(`([0-9]+\.[0-9]+)%`)

// ScanCRLF is a custom bufio.SplitFunc that splits tokens on '\r', '\n', or "\r\n" for NCNN stderr progress.
// Example: scanner.Split(pipeline.ScanCRLF)
func ScanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		adv := i + 1
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			adv = i + 2
		}
		return adv, data[0:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ParseProgressPercent extracts a floating-point percentage from an NCNN stderr token.
// Example: pct, ok := pipeline.ParseProgressPercent("64.20%")
func ParseProgressPercent(line string) (float64, bool) {
	normalized := strings.ReplaceAll(line, ",", ".")
	matches := progressRegex.FindStringSubmatch(normalized)
	if len(matches) < 2 {
		return 0, false
	}
	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil || val < 0 || val > MaxProgressPercent {
		return 0, false
	}
	return val, true
}

// PrimeOffloadEnv returns a new environment slice with NVIDIA PRIME Render Offload variables appended.
// Example: env := pipeline.PrimeOffloadEnv(os.Environ())
func PrimeOffloadEnv(baseEnv []string) []string {
	env := make([]string, 0, len(baseEnv)+2)
	env = append(env, baseEnv...)
	env = append(env, PrimeOffloadEnvVar, GlxVendorNvidiaVar)
	return env
}

// BuildUpscaleArgs constructs the explicit NCNN Vulkan arguments for the configured engine and VRAM profile.
// Example: args := pipeline.BuildUpscaleArgs(cfg, inDir, outDir)
func BuildUpscaleArgs(cfg config.RunConfig, inDir, outDir string) []string {
	preset := config.PresetForProfile(cfg.Profile)
	modelFlag := "-n"
	if cfg.Engine == config.EngineRealCUGAN {
		modelFlag = "-m"
	}
	return []string{
		"-i", inDir,
		"-o", outDir,
		modelFlag, cfg.Model,
		"-s", strconv.Itoa(cfg.Scale),
		"-t", strconv.Itoa(preset.TileSize),
		"-j", preset.Threads,
		"-f", "png",
	}
}

// UpscaleChunk executes the NCNN Vulkan engine on inDir -> outDir and emits monotonic chunk progress percentages.
// Why combine outDir PNG count with stderr tile %: NCNN directory mode prints per-frame tile progress (0% -> 50% -> 0%),
// not directory-wide progress; combining completed output frames in /dev/shm with current frame tile % avoids oscillation.
// Example: err := pipeline.UpscaleChunk(ctx, exec, "realesrgan-ncnn-vulkan", cfg, inDir, outDir, onProgress)
func UpscaleChunk(
	ctx context.Context,
	exec ProcessExecutor,
	enginePath string,
	cfg config.RunConfig,
	inDir, outDir string,
	onProgress func(float64),
) error {
	args := BuildUpscaleArgs(cfg, inDir, outDir)
	env := PrimeOffloadEnv(os.Environ())
	inCount := countPNGFiles(inDir)
	tracker := newMonotonicTracker(inCount, outDir, onProgress)

	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go tracker.pollOutputDirectory(pollCtx)

	err := exec.RunWithStderr(ctx, env, func(line string) {
		if tilePct, ok := ParseProgressPercent(line); ok {
			tracker.reportTile(tilePct)
		}
	}, enginePath, args...)
	if err != nil {
		return fmt.Errorf("ncnn upscaler %q failed: %w", enginePath, err)
	}
	return nil
}

type monotonicTracker struct {
	mu         sync.Mutex
	inCount    int
	outDir     string
	maxPct     float64
	hasEmitted bool
	onProgress func(float64)
}

func newMonotonicTracker(inCount int, outDir string, onProgress func(float64)) *monotonicTracker {
	return &monotonicTracker{inCount: inCount, outDir: outDir, onProgress: onProgress}
}

func (t *monotonicTracker) reportTile(tilePct float64) {
	if t.onProgress == nil {
		return
	}
	outCount := countPNGFiles(t.outDir)
	raw := computeChunkProgress(t.inCount, outCount, tilePct)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasEmitted || raw > t.maxPct {
		t.maxPct = raw
		t.hasEmitted = true
		t.onProgress(t.maxPct)
	}
}

func (t *monotonicTracker) pollOutputDirectory(ctx context.Context) {
	if t.inCount <= 0 || t.onProgress == nil {
		return
	}
	ticker := time.NewTicker(OutDirPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.reportTile(0)
		}
	}
}

func computeChunkProgress(inCount, outCount int, tilePct float64) float64 {
	if inCount <= 0 {
		return tilePct
	}
	if outCount >= inCount {
		return MaxProgressPercent
	}
	pct := ((float64(outCount) + (tilePct / MaxProgressPercent)) / float64(inCount)) * MaxProgressPercent
	if pct > MaxProgressPercent {
		return MaxProgressPercent
	}
	return pct
}

func countPNGFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			count++
		}
	}
	return count
}
