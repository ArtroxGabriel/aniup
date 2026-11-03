package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/state"
)

func TestWorkDirPathFormatsHiddenDirectoryBesideInput(t *testing.T) {
	// Arrange
	input := "/home/user/anime/ep01.mkv"

	// Act
	workDir := state.WorkDirPath(input, 2)

	// Assert
	expected := "/home/user/anime/.aniup_ep01.mkv_2x"
	if workDir != expected {
		t.Fatalf("expected %q, got %q", expected, workDir)
	}
}

func TestComputePartialSHA256ProducesDeterministicHashForSmallAndLargeFiles(t *testing.T) {
	// Arrange small file
	tmpDir := t.TempDir()
	smallFile := filepath.Join(tmpDir, "small.mkv")
	if err := os.WriteFile(smallFile, []byte("anime-video-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Act
	hashSmall1, err := state.ComputePartialSHA256(smallFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hashSmall2, err := state.ComputeFileSHA256(smallFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert
	if hashSmall1 == "" || hashSmall2 == "" {
		t.Fatal("expected non-empty hashes")
	}

	// Arrange large file (> 8 MiB: 10 MiB)
	largeFile := filepath.Join(tmpDir, "large.mkv")
	buf := make([]byte, 10*1024*1024)
	buf[0] = 0xAA
	buf[len(buf)-1] = 0xBB
	if err := os.WriteFile(largeFile, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	// Act: hash before and after mutating middle byte (at 5 MiB, outside first/last 4 MiB)
	hashLarge1, err := state.ComputePartialSHA256(largeFile)
	if err != nil {
		t.Fatal(err)
	}
	buf[5*1024*1024] = 0xFF
	if err := os.WriteFile(largeFile, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	hashLargeMiddleChanged, err := state.ComputePartialSHA256(largeFile)
	if err != nil {
		t.Fatal(err)
	}

	// Assert: middle byte outside first/last 4 MiB does not alter partial hash
	if hashLarge1 != hashLargeMiddleChanged {
		t.Fatalf("expected partial hash to ignore middle bytes, got %s vs %s", hashLarge1, hashLargeMiddleChanged)
	}

	// Mutate last byte -> must change partial hash
	buf[len(buf)-1] = 0xCC
	if err := os.WriteFile(largeFile, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	hashLargeTailChanged, err := state.ComputePartialSHA256(largeFile)
	if err != nil {
		t.Fatal(err)
	}
	if hashLarge1 == hashLargeTailChanged {
		t.Fatal("expected tail mutation to change partial SHA256")
	}
}

func TestManifestLifecycleSaveLoadCompatibilityAndMarkCompleted(t *testing.T) {
	// Arrange
	workDir := filepath.Join(t.TempDir(), ".aniup_ep01.mkv_2x")
	cfg := config.RunConfig{
		InputPath: "/videos/ep01.mkv",
		Engine:    config.EngineRealESRGAN,
		Model:     "realesr-animevideov3",
		Scale:     2,
		Encoder:   config.EncoderHEVCNVENC,
		ChunkSize: 600,
	}

	// Act: create and save manifest for 1250 frames (3 chunks: 0..599, 600..1199, 1200..1249)
	m := state.NewManifest(cfg, "input-sha256", 1250, "24000/1001")
	if err := state.SaveManifest(workDir, m); err != nil {
		t.Fatalf("SaveManifest failed: %v", err)
	}

	// Assert chunks layout
	if len(m.Chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(m.Chunks))
	}
	if m.Chunks[0].StartFrame != 0 || m.Chunks[0].EndFrame != 599 {
		t.Fatalf("unexpected chunk 0 bounds: %+v", m.Chunks[0])
	}
	if m.Chunks[2].StartFrame != 1200 || m.Chunks[2].EndFrame != 1249 {
		t.Fatalf("unexpected chunk 2 bounds: %+v", m.Chunks[2])
	}

	// Mark chunk 0 completed and reload from disk
	updated, err := state.MarkChunkCompleted(workDir, m, 0, "seg_00001.mkv", "seg-sha-1")
	if err != nil {
		t.Fatalf("MarkChunkCompleted failed: %v", err)
	}
	if m.CompletedCount() != 0 || updated.CompletedCount() != 1 {
		t.Fatalf("expected original manifest to remain immutable (0) and updated to have 1 completed, got orig=%d updated=%d", m.CompletedCount(), updated.CompletedCount())
	}
	loaded, err := state.LoadManifest(workDir)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if loaded.CompletedCount() != 1 {
		t.Fatalf("expected 1 completed chunk, got %d", loaded.CompletedCount())
	}
	if !state.IsCompatible(loaded, "input-sha256", cfg) {
		t.Fatal("expected loaded manifest to be compatible with identical config and hash")
	}
	if state.IsCompatible(loaded, "different-sha256", cfg) {
		t.Fatal("expected manifest to be incompatible when input hash differs")
	}
}
