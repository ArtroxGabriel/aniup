package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/gabrigas/aniup/internal/config"
)

const (
	PrimeOffloadEnvVar = "__NV_PRIME_RENDER_OFFLOAD=1"
	GlxVendorNvidiaVar = "__GLX_VENDOR_LIBRARY_NAME=nvidia"
	MaxProgressPercent = 100.0
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

// UpscaleChunk executes the NCNN Vulkan engine on inDir -> outDir and emits parsed progress percentages.
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
	err := exec.RunWithStderr(ctx, env, func(line string) {
		if pct, ok := ParseProgressPercent(line); ok && onProgress != nil {
			onProgress(pct)
		}
	}, enginePath, args...)
	if err != nil {
		return fmt.Errorf("ncnn upscaler %q failed: %w", enginePath, err)
	}
	return nil
}
