package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/state"
)

const (
	ConcatFileName = "concat.txt"
	DefaultPixFmt  = "yuv420p10le"
)

// BuildEncodeChunkArgs constructs FFmpeg NVENC arguments to encode upscaled PNG frames into a temporary MKV segment.
// Example: args := pipeline.BuildEncodeChunkArgs("/dev/shm/out/frame_%08d.png", "24000/1001", config.EncoderHEVCNVENC, tmpPath)
func BuildEncodeChunkArgs(inPattern, frameRateRatio string, encoder config.EncoderType, tmpOutputPath string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-framerate", frameRateRatio,
		"-i", inPattern,
		"-c:v", string(encoder),
		"-preset", config.DefaultPreset,
		"-rc", "vbr",
		"-cq", strconv.Itoa(config.DefaultNVENCQ),
		"-b:v", "0",
		"-pix_fmt", DefaultPixFmt,
		tmpOutputPath,
	}
}

// EncodeChunkAtomic encodes PNGs from outDir into tmp_<segFileName>, atomically renames it to <segFileName>, and returns its SHA256.
// Example: sum, err := pipeline.EncodeChunkAtomic(ctx, exec, "ffmpeg", outDir, "24000/1001", cfg.Encoder, workDir, "seg_00001.mkv")
func EncodeChunkAtomic(
	ctx context.Context,
	exec ProcessExecutor,
	ffmpegPath, outDir, frameRateRatio string,
	encoder config.EncoderType,
	workDir, segFileName string,
) (string, error) {
	inPattern := filepath.Join(outDir, FrameFilePattern)
	tmpPath := filepath.Join(workDir, "tmp_"+segFileName)
	finalPath := filepath.Join(workDir, segFileName)

	args := BuildEncodeChunkArgs(inPattern, frameRateRatio, encoder, tmpPath)
	if err := exec.Run(ctx, nil, ffmpegPath, args...); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to encode segment %q: %w", segFileName, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", fmt.Errorf("failed to rename atomic segment %q -> %q: %w", tmpPath, finalPath, err)
	}
	segHash, err := state.ComputeFileSHA256(finalPath)
	if err != nil {
		return "", fmt.Errorf("failed to hash segment %q: %w", finalPath, err)
	}
	return segHash, nil
}

// WriteConcatList writes the FFmpeg concat demuxer manifest (concat.txt) inside workDir.
// Example: concatPath, err := pipeline.WriteConcatList(workDir, manifest.Chunks)
func WriteConcatList(workDir string, chunks []state.ChunkRecord) (string, error) {
	var b strings.Builder
	for _, c := range chunks {
		escaped := strings.ReplaceAll(c.SegmentFile, "'", "'\\''")
		_, _ = fmt.Fprintf(&b, "file '%s'\n", escaped)
	}
	concatPath := filepath.Join(workDir, ConcatFileName)
	if err := os.WriteFile(concatPath, []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("failed to write concat list %q: %w", concatPath, err)
	}
	return concatPath, nil
}

// BuildFinalConcatArgs constructs the FFmpeg concat demuxer command that merges segments with original audio, subtitles, and chapters.
// Example: args := pipeline.BuildFinalConcatArgs(concatPath, inputPath, outputPath)
func BuildFinalConcatArgs(concatTxtPath, inputVideoPath, outputVideoPath string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", concatTxtPath,
		"-i", inputVideoPath,
		"-map", "0:v:0",
		"-map", "1:a?",
		"-map", "1:s?",
		"-map_chapters", "1",
		"-c", "copy",
		outputVideoPath,
	}
}

// ConcatFinalVideo generates concat.txt and muxes all completed video chunks with the original streams into outputVideoPath.
// Example: err := pipeline.ConcatFinalVideo(ctx, exec, "ffmpeg", workDir, manifest.Chunks, cfg.InputPath, cfg.OutputPath)
func ConcatFinalVideo(
	ctx context.Context,
	exec ProcessExecutor,
	ffmpegPath, workDir string,
	chunks []state.ChunkRecord,
	inputVideoPath, outputVideoPath string,
) error {
	concatPath, err := WriteConcatList(workDir, chunks)
	if err != nil {
		return err
	}
	args := BuildFinalConcatArgs(concatPath, inputVideoPath, outputVideoPath)
	if err := exec.Run(ctx, nil, ffmpegPath, args...); err != nil {
		return fmt.Errorf("final ffmpeg concat failed for %q: %w", outputVideoPath, err)
	}
	return nil
}
