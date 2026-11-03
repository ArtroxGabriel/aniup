package deps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/deps"
)

// FakeSystemInspector is a named fake implementation of deps.SystemInspector for unit testing.
type FakeSystemInspector struct {
	Binaries    map[string]string
	EncodersOut string
	EncodersErr error
	ShmFree     uint64
	ShmWritable bool
	ShmErr      error
}

func (f *FakeSystemInspector) LookPath(file string) (string, error) {
	if path, ok := f.Binaries[file]; ok && path != "" {
		return path, nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *FakeSystemInspector) RunCommand(_ context.Context, name string, args ...string) (string, error) {
	if strings.Contains(name, "ffmpeg") && len(args) >= 2 && args[1] == "-encoders" {
		return f.EncodersOut, f.EncodersErr
	}
	return "", errors.New("unexpected command")
}

func (f *FakeSystemInspector) ShmStatus(_ string) (uint64, bool, error) {
	return f.ShmFree, f.ShmWritable, f.ShmErr
}

func TestCheckAllPassesWhenAllMandatoryDependenciesExist(t *testing.T) {
	// Arrange
	fake := &FakeSystemInspector{
		Binaries: map[string]string{
			"ffmpeg":                 "/usr/bin/ffmpeg",
			"ffprobe":                "/usr/bin/ffprobe",
			"realesrgan-ncnn-vulkan": "/usr/bin/realesrgan-ncnn-vulkan",
			"nvidia-smi":             "/usr/bin/nvidia-smi",
		},
		EncodersOut: " V....D hevc_nvenc NVIDIA NVENC hevc\n V....D av1_nvenc NVIDIA NVENC av1\n",
		ShmFree:     8 * 1024 * 1024 * 1024,
		ShmWritable: true,
	}

	// Act
	report := deps.CheckAll(context.Background(), fake, "/dev/shm")

	// Assert
	if !report.IsOK() {
		t.Fatalf("expected report to be OK, got missing: %+v", report.MissingMandatory)
	}
	engines := report.AvailableEngines()
	if len(engines) != 1 || engines[0] != config.EngineRealESRGAN {
		t.Fatalf("expected only realesrgan engine available, got %v", engines)
	}
}

func TestCheckAllReportsArchInstallCommandsWhenDependenciesMissing(t *testing.T) {
	// Arrange
	fake := &FakeSystemInspector{
		Binaries:    map[string]string{},
		EncodersOut: "",
		ShmFree:     1 * 1024 * 1024 * 1024, // 1 GiB (< 4 GiB minimum)
		ShmWritable: true,
	}

	// Act
	report := deps.CheckAll(context.Background(), fake, "/dev/shm")

	// Assert
	if report.IsOK() {
		t.Fatal("expected report to fail when dependencies are missing")
	}

	var combinedCommands strings.Builder
	for _, m := range report.MissingMandatory {
		combinedCommands.WriteString(m.InstallCommand + "\n")
	}
	allCmds := combinedCommands.String()

	expectedSnippets := []string{
		"sudo pacman -S ffmpeg",
		"sudo pacman -S nvidia-utils",
		"paru -S realesrgan-ncnn-vulkan-bin",
		"paru -S realcugan-ncnn-vulkan-bin",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(allCmds, snippet) {
			t.Fatalf("expected missing dependencies to include %q, got:\n%s", snippet, allCmds)
		}
	}
}

func TestOSSystemInspectorShmStatusReadsTempDirAndMissingNVENC(t *testing.T) {
	// Arrange
	inspector := deps.NewOSSystemInspector()
	tmpDir := t.TempDir()

	// Act
	freeBytes, writable, err := inspector.ShmStatus(tmpDir)

	// Assert
	if err != nil || !writable || freeBytes == 0 {
		t.Fatalf("expected writable temp dir with free bytes, got free=%d writable=%v err=%v", freeBytes, writable, err)
	}

	// Arrange fake with ffmpeg present but nvenc missing and unwritable shm
	fake := &FakeSystemInspector{
		Binaries: map[string]string{
			"ffmpeg":                "/usr/bin/ffmpeg",
			"ffprobe":               "/usr/bin/ffprobe",
			"realcugan-ncnn-vulkan": "/usr/bin/realcugan-ncnn-vulkan",
		},
		EncodersOut: " V....D libx264",
		ShmFree:     8 * 1024 * 1024 * 1024,
		ShmWritable: false,
	}

	// Act
	report := deps.CheckAll(context.Background(), fake, "")

	// Assert
	if report.IsOK() {
		t.Fatal("expected report to fail when nvenc is missing and shm is unwritable")
	}
	if len(report.AvailableEngines()) != 1 || report.AvailableEngines()[0] != config.EngineRealCUGAN {
		t.Fatalf("expected realcugan engine, got %v", report.AvailableEngines())
	}
}
