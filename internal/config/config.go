package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// EngineType identifies the NCNN Vulkan upscaler binary.
type EngineType string

const (
	EngineRealESRGAN EngineType = "realesrgan-ncnn-vulkan"
	EngineRealCUGAN  EngineType = "realcugan-ncnn-vulkan"
)

// VRAMProfile identifies the GPU tiling and thread concurrency preset.
type VRAMProfile string

const (
	ProfileTurbo        VRAMProfile = "turbo"
	ProfileBalanced     VRAMProfile = "balanced"
	ProfileConservative VRAMProfile = "conservative"
)

// EncoderType identifies the NVIDIA NVENC video encoder.
type EncoderType string

const (
	EncoderHEVCNVENC EncoderType = "hevc_nvenc"
	EncoderAV1NVENC  EncoderType = "av1_nvenc"
)

const (
	DefaultScale     = 2
	AlternateScale   = 4
	DefaultChunkSize = 600
	DefaultNVENCQ    = 19
	DefaultPreset    = "p6"

	TurboTileSize        = 768
	BalancedTileSize     = 400
	ConservativeTileSize = 256

	TurboThreads        = "2:3:3"
	BalancedThreads     = "1:2:2"
	ConservativeThreads = "1:1:1"

	// PNGBytesPerPixelNumerator / PNGBytesPerPixelDenominator approximates compressed anime PNG size (~0.25 B/px).
	PNGBytesPerPixelNumerator   = 1
	PNGBytesPerPixelDenominator = 4
)

// GPUPreset holds the NCNN tile size and load:proc:save thread specification.
type GPUPreset struct {
	TileSize int
	Threads  string
}

// RunConfig holds the immutable runtime parameters for an upscale job.
type RunConfig struct {
	InputPath   string
	OutputPath  string
	Engine      EngineType
	Model       string
	Scale       int
	Profile     VRAMProfile
	ChunkSize   int
	Encoder     EncoderType
	Resume      bool
	KeepChunks  bool
	InputWidth  int
	InputHeight int
}

// ModelsForEngine returns the supported models for a given NCNN engine.
// Example: models := config.ModelsForEngine(config.EngineRealESRGAN)
func ModelsForEngine(engine EngineType) []string {
	switch engine {
	case EngineRealESRGAN:
		return []string{"realesr-animevideov3", "realesrgan-x4plus-anime"}
	case EngineRealCUGAN:
		return []string{"models-se", "models-pro"}
	default:
		return nil
	}
}

// PresetForProfile returns the NCNN tile size and thread layout for a VRAM profile.
// Example: preset := config.PresetForProfile(config.ProfileTurbo)
func PresetForProfile(profile VRAMProfile) GPUPreset {
	switch profile {
	case ProfileTurbo:
		return GPUPreset{TileSize: TurboTileSize, Threads: TurboThreads}
	case ProfileBalanced:
		return GPUPreset{TileSize: BalancedTileSize, Threads: BalancedThreads}
	default:
		return GPUPreset{TileSize: ConservativeTileSize, Threads: ConservativeThreads}
	}
}

// EstimateShmBytes estimates the peak /dev/shm usage in bytes for a chunk's input and output PNGs.
// Example: bytes := config.EstimateShmBytes(1920, 1080, 2, 600)
func EstimateShmBytes(width, height, scale, chunkSize int) uint64 {
	if width <= 0 || height <= 0 || scale <= 0 || chunkSize <= 0 {
		return 0
	}
	inPixels := uint64(width) * uint64(height)
	outPixels := inPixels * uint64(scale*scale)
	totalPixelsPerFrame := inPixels + outPixels
	bytesPerFrame := (totalPixelsPerFrame * PNGBytesPerPixelNumerator) / PNGBytesPerPixelDenominator
	return bytesPerFrame * uint64(chunkSize)
}

// DefaultOutputPath builds the default output MKV path alongside the input video.
// Example: out := config.DefaultOutputPath("/videos/ep01.mkv", 2, "realesr-animevideov3")
func DefaultOutputPath(inputPath string, scale int, model string) string {
	dir := filepath.Dir(inputPath)
	base := filepath.Base(inputPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, fmt.Sprintf("%s_%dx_%s.mkv", stem, scale, model))
}

// ValidateConfig checks all RunConfig invariants and verifies sufficient /dev/shm free space.
// Example: err := config.ValidateConfig(cfg, freeShmBytes)
func ValidateConfig(cfg RunConfig, freeShmBytes uint64) error {
	if strings.TrimSpace(cfg.InputPath) == "" {
		return fmt.Errorf("invalid input path %q: expected non-empty video file path", cfg.InputPath)
	}
	models := ModelsForEngine(cfg.Engine)
	if len(models) == 0 {
		return fmt.Errorf("invalid engine %q: expected %q or %q", cfg.Engine, EngineRealESRGAN, EngineRealCUGAN)
	}
	if !slices.Contains(models, cfg.Model) {
		return fmt.Errorf("invalid model %q for engine %q: expected one of %v", cfg.Model, cfg.Engine, models)
	}
	if cfg.Scale != DefaultScale && cfg.Scale != AlternateScale {
		return fmt.Errorf("invalid scale %d: expected %d or %d", cfg.Scale, DefaultScale, AlternateScale)
	}
	if cfg.Profile != ProfileTurbo && cfg.Profile != ProfileBalanced && cfg.Profile != ProfileConservative {
		return fmt.Errorf("invalid profile %q: expected %q, %q, or %q", cfg.Profile, ProfileTurbo, ProfileBalanced, ProfileConservative)
	}
	return validateResourcesAndEncoder(cfg, freeShmBytes)
}

func validateResourcesAndEncoder(cfg RunConfig, freeShmBytes uint64) error {
	if cfg.ChunkSize <= 0 {
		return fmt.Errorf("invalid chunk size %d: expected positive integer (> 0)", cfg.ChunkSize)
	}
	if cfg.Encoder != EncoderHEVCNVENC && cfg.Encoder != EncoderAV1NVENC {
		return fmt.Errorf("invalid encoder %q: expected %q or %q", cfg.Encoder, EncoderHEVCNVENC, EncoderAV1NVENC)
	}
	estimated := EstimateShmBytes(cfg.InputWidth, cfg.InputHeight, cfg.Scale, cfg.ChunkSize)
	if estimated > 0 && estimated > freeShmBytes {
		return fmt.Errorf("insufficient /dev/shm space: chunk requires ~%d MiB, but only %d MiB available",
			estimated/(1024*1024), freeShmBytes/(1024*1024))
	}
	return nil
}
