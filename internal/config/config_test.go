package config_test

import (
	"strings"
	"testing"

	"github.com/gabrigas/aniup/internal/config"
)

func TestModelsForEngineReturnsExpectedModels(t *testing.T) {
	// Arrange & Act
	esrganModels := config.ModelsForEngine(config.EngineRealESRGAN)
	cuganModels := config.ModelsForEngine(config.EngineRealCUGAN)
	unknownModels := config.ModelsForEngine(config.EngineType("unknown"))

	// Assert
	if len(esrganModels) != 2 || esrganModels[0] != "realesr-animevideov3" || esrganModels[1] != "realesrgan-x4plus-anime" {
		t.Fatalf("unexpected realesrgan models: %v", esrganModels)
	}
	if len(cuganModels) != 2 || cuganModels[0] != "models-se" || cuganModels[1] != "models-pro" {
		t.Fatalf("unexpected realcugan models: %v", cuganModels)
	}
	if len(unknownModels) != 0 {
		t.Fatalf("expected empty models for unknown engine, got %v", unknownModels)
	}
}

func TestPresetForProfileReturnsSpecTileAndThreads(t *testing.T) {
	// Arrange & Act
	turbo := config.PresetForProfile(config.ProfileTurbo)
	balanced := config.PresetForProfile(config.ProfileBalanced)
	conservative := config.PresetForProfile(config.ProfileConservative)

	// Assert
	if turbo.TileSize != 768 || turbo.Threads != "2:3:3" {
		t.Fatalf("unexpected turbo preset: %+v", turbo)
	}
	if balanced.TileSize != 400 || balanced.Threads != "1:2:2" {
		t.Fatalf("unexpected balanced preset: %+v", balanced)
	}
	if conservative.TileSize != 256 || conservative.Threads != "1:1:1" {
		t.Fatalf("unexpected conservative preset: %+v", conservative)
	}
}

func TestEstimateShmBytesScalesWithResolutionAndScale(t *testing.T) {
	// Arrange
	width, height := 1920, 1080
	chunkSize := 600

	// Act
	bytes2x := config.EstimateShmBytes(width, height, 2, chunkSize)
	bytes4x := config.EstimateShmBytes(width, height, 4, chunkSize)

	// Assert
	if bytes2x == 0 {
		t.Fatal("expected non-zero shm estimate for 2x")
	}
	if bytes4x <= bytes2x {
		t.Fatalf("expected 4x estimate (%d) > 2x estimate (%d)", bytes4x, bytes2x)
	}
}

func TestDefaultOutputPathGeneratesExpectedName(t *testing.T) {
	// Arrange
	input := "/videos/episode01.mkv"

	// Act
	out := config.DefaultOutputPath(input, 2, "realesr-animevideov3")

	// Assert
	expected := "/videos/episode01_2x_realesr-animevideov3.mkv"
	if out != expected {
		t.Fatalf("expected %q, got %q", expected, out)
	}
}

func TestValidateConfigRejectsInvalidParametersAndInsufficientShm(t *testing.T) {
	// Arrange
	validCfg := config.RunConfig{
		InputPath:   "/videos/ep1.mkv",
		OutputPath:  "/videos/ep1_2x.mkv",
		Engine:      config.EngineRealESRGAN,
		Model:       "realesr-animevideov3",
		Scale:       2,
		Profile:     config.ProfileTurbo,
		ChunkSize:   600,
		Encoder:     config.EncoderHEVCNVENC,
		InputWidth:  1920,
		InputHeight: 1080,
	}
	plentyShm := uint64(8) * 1024 * 1024 * 1024

	// Act & Assert: valid
	if err := config.ValidateConfig(validCfg, plentyShm); err != nil {
		t.Fatalf("expected valid config to pass, got: %v", err)
	}

	// Act & Assert: invalid scale
	badScale := validCfg
	badScale.Scale = 3
	if err := config.ValidateConfig(badScale, plentyShm); err == nil || !strings.Contains(err.Error(), "scale") {
		t.Fatalf("expected scale validation error, got: %v", err)
	}

	// Act & Assert: invalid model for engine
	badModel := validCfg
	badModel.Model = "models-se"
	if err := config.ValidateConfig(badModel, plentyShm); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected model validation error, got: %v", err)
	}

	// Act & Assert: insufficient /dev/shm
	tinyShm := uint64(1024)
	if err := config.ValidateConfig(validCfg, tinyShm); err == nil || !strings.Contains(err.Error(), "/dev/shm") {
		t.Fatalf("expected /dev/shm space validation error, got: %v", err)
	}

	// Act & Assert: empty input, invalid engine, invalid chunk size, invalid encoder
	emptyInput := validCfg
	emptyInput.InputPath = "   "
	if err := config.ValidateConfig(emptyInput, plentyShm); err == nil {
		t.Fatal("expected error for empty input path")
	}

	badEngine := validCfg
	badEngine.Engine = "bad"
	if err := config.ValidateConfig(badEngine, plentyShm); err == nil {
		t.Fatal("expected error for invalid engine")
	}

	badChunk := validCfg
	badChunk.ChunkSize = 0
	if err := config.ValidateConfig(badChunk, plentyShm); err == nil {
		t.Fatal("expected error for zero chunk size")
	}

	badEncoder := validCfg
	badEncoder.Encoder = "libx264"
	if err := config.ValidateConfig(badEncoder, plentyShm); err == nil {
		t.Fatal("expected error for invalid encoder")
	}

	badProfile := validCfg
	badProfile.Profile = "invalid-profile"
	if err := config.ValidateConfig(badProfile, plentyShm); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("expected error for invalid profile, got: %v", err)
	}

	if got := config.EstimateShmBytes(0, 1080, 2, 600); got != 0 {
		t.Fatalf("expected 0 for non-positive dimensions, got %d", got)
	}
}
