package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
)

const FrameFilePattern = "frame_%08d.png"

// BuildExtractArgs constructs the explicit FFmpeg arguments to extract frames [startFrame, endFrame] into outPattern.
// Example: args := pipeline.BuildExtractArgs("/videos/ep1.mkv", "/dev/shm/aniup_123/in/frame_%08d.png", 0, 599)
func BuildExtractArgs(inputPath, outPattern string, startFrame, endFrame int) []string {
	filter := fmt.Sprintf("select='between(n\\,%d\\,%d)',setpts=N/FRAME_RATE/TB", startFrame, endFrame)
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-i", inputPath,
		"-vf", filter,
		"-fps_mode", "passthrough",
		"-start_number", "1",
		outPattern,
	}
}

// ExtractChunk runs FFmpeg to extract the frame window [startFrame, endFrame] into inDir as PNG files.
// Example: err := pipeline.ExtractChunk(ctx, exec, "ffmpeg", "/videos/ep1.mkv", inDir, 0, 599)
func ExtractChunk(
	ctx context.Context,
	exec ProcessExecutor,
	ffmpegPath, inputPath, inDir string,
	startFrame, endFrame int,
) error {
	outPattern := filepath.Join(inDir, FrameFilePattern)
	args := BuildExtractArgs(inputPath, outPattern, startFrame, endFrame)
	if err := exec.Run(ctx, nil, ffmpegPath, args...); err != nil {
		return fmt.Errorf("failed to extract chunk frames [%d..%d] from %q: %w", startFrame, endFrame, inputPath, err)
	}
	return nil
}
