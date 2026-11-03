package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CommandRunner executes an external command and returns its stdout/stderr text.
type CommandRunner interface {
	RunCommand(ctx context.Context, name string, args ...string) (string, error)
}

// VideoMetadata holds the essential video stream and container parameters extracted via ffprobe.
type VideoMetadata struct {
	Width           int
	Height          int
	FrameRateRatio  string
	FPS             float64
	TotalFrames     int
	DurationSec     float64
	VideoCodec      string
	AudioStreams    int
	SubtitleStreams int
}

type ffprobeResponse struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	CodecType    string            `json:"codec_type"`
	CodecName    string            `json:"codec_name"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	RFrameRate   string            `json:"r_frame_rate"`
	AvgFrameRate string            `json:"avg_frame_rate"`
	NBFrames     string            `json:"nb_frames"`
	Duration     string            `json:"duration"`
	Tags         map[string]string `json:"tags"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
}

// ProbeFile runs ffprobe on inputPath and parses its JSON output into VideoMetadata.
// Example: meta, err := probe.ProbeFile(ctx, runner, "ffprobe", "/videos/ep01.mkv")
func ProbeFile(ctx context.Context, runner CommandRunner, ffprobePath, inputPath string) (VideoMetadata, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	out, err := runner.RunCommand(
		ctx,
		ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		inputPath,
	)
	if err != nil {
		return VideoMetadata{}, fmt.Errorf("ffprobe failed for %q: %w", inputPath, err)
	}
	return ParseFFprobeJSON([]byte(out))
}

// ParseFFprobeJSON parses raw ffprobe JSON bytes into a validated VideoMetadata struct.
// Example: meta, err := probe.ParseFFprobeJSON(rawJSON)
func ParseFFprobeJSON(rawJSON []byte) (VideoMetadata, error) {
	var parsed ffprobeResponse
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return VideoMetadata{}, fmt.Errorf("failed to unmarshal ffprobe JSON: %w", err)
	}
	videoStream, audioCount, subCount, err := selectStreams(parsed.Streams)
	if err != nil {
		return VideoMetadata{}, err
	}
	ratio, fps, err := parseFrameRate(videoStream.RFrameRate, videoStream.AvgFrameRate)
	if err != nil {
		return VideoMetadata{}, err
	}
	durationSec := parseDurationSeconds(videoStream.Duration, parsed.Format.Duration)
	totalFrames := resolveTotalFrames(videoStream, durationSec, fps)
	return VideoMetadata{
		Width:           videoStream.Width,
		Height:          videoStream.Height,
		FrameRateRatio:  ratio,
		FPS:             fps,
		TotalFrames:     totalFrames,
		DurationSec:     durationSec,
		VideoCodec:      videoStream.CodecName,
		AudioStreams:    audioCount,
		SubtitleStreams: subCount,
	}, nil
}

func selectStreams(streams []ffprobeStream) (ffprobeStream, int, int, error) {
	var video ffprobeStream
	hasVideo := false
	audioCount := 0
	subCount := 0
	for _, s := range streams {
		switch s.CodecType {
		case "video":
			if !hasVideo {
				video = s
				hasVideo = true
			}
		case "audio":
			audioCount++
		case "subtitle":
			subCount++
		}
	}
	if !hasVideo || video.Width <= 0 || video.Height <= 0 {
		return ffprobeStream{}, 0, 0, fmt.Errorf("no valid video stream found in ffprobe output (width=%d, height=%d)", video.Width, video.Height)
	}
	return video, audioCount, subCount, nil
}

func parseFrameRate(rFrameRate, avgFrameRate string) (string, float64, error) {
	raw := strings.TrimSpace(rFrameRate)
	if raw == "" || raw == "0/0" {
		raw = strings.TrimSpace(avgFrameRate)
	}
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		num, errNum := strconv.ParseFloat(parts[0], 64)
		den, errDen := strconv.ParseFloat(parts[1], 64)
		if errNum != nil || errDen != nil || num <= 0 || den <= 0 {
			return "", 0, fmt.Errorf("invalid rational frame rate %q: expected positive num/den", raw)
		}
		return raw, num / den, nil
	}
	fps, err := strconv.ParseFloat(raw, 64)
	if err != nil || fps <= 0 {
		return "", 0, fmt.Errorf("invalid frame rate %q: expected positive number or ratio", raw)
	}
	return raw, fps, nil
}

func parseDurationSeconds(streamDuration, formatDuration string) float64 {
	if d, err := strconv.ParseFloat(strings.TrimSpace(streamDuration), 64); err == nil && d > 0 {
		return d
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(formatDuration), 64); err == nil && d > 0 {
		return d
	}
	return 0
}

func resolveTotalFrames(video ffprobeStream, durationSec, fps float64) int {
	if n, err := strconv.Atoi(strings.TrimSpace(video.NBFrames)); err == nil && n > 0 {
		return n
	}
	for k, v := range video.Tags {
		if strings.HasPrefix(strings.ToUpper(k), "NUMBER_OF_FRAMES") {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n
			}
		}
	}
	if durationSec > 0 && fps > 0 {
		return int(math.Round(durationSec * fps))
	}
	return 1
}
