package probe_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/gabrigas/aniup/internal/probe"
)

// FakeProbeRunner is a named fake implementation of probe.CommandRunner for unit tests.
type FakeProbeRunner struct {
	Output       string
	Err          error
	CapturedName string
	CapturedArgs []string
}

func (f *FakeProbeRunner) RunCommand(_ context.Context, name string, args ...string) (string, error) {
	f.CapturedName = name
	f.CapturedArgs = append([]string(nil), args...)
	return f.Output, f.Err
}

func TestParseFFprobeJSONExtractsResolutionFrameRateAndStreams(t *testing.T) {
	// Arrange
	rawJSON := []byte(`{
		"streams": [
			{
				"codec_type": "video",
				"codec_name": "hevc",
				"width": 1920,
				"height": 1080,
				"r_frame_rate": "24000/1001",
				"nb_frames": "1440"
			},
			{"codec_type": "audio", "codec_name": "aac"},
			{"codec_type": "audio", "codec_name": "flac"},
			{"codec_type": "subtitle", "codec_name": "ass"}
		],
		"format": {
			"duration": "60.060000"
		}
	}`)

	// Act
	meta, err := probe.ParseFFprobeJSON(rawJSON)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Width != 1920 || meta.Height != 1080 {
		t.Fatalf("unexpected dimensions: %dx%d", meta.Width, meta.Height)
	}
	if meta.FrameRateRatio != "24000/1001" {
		t.Fatalf("expected ratio 24000/1001, got %q", meta.FrameRateRatio)
	}
	if math.Abs(meta.FPS-23.976023) > 0.001 {
		t.Fatalf("unexpected FPS: %f", meta.FPS)
	}
	if meta.TotalFrames != 1440 {
		t.Fatalf("expected 1440 frames, got %d", meta.TotalFrames)
	}
	if meta.AudioStreams != 2 || meta.SubtitleStreams != 1 {
		t.Fatalf("unexpected stream counts: audio=%d subs=%d", meta.AudioStreams, meta.SubtitleStreams)
	}
}

func TestParseFFprobeJSONFallsBackToMKVTagsAndDuration(t *testing.T) {
	// Arrange: MKV with NUMBER_OF_FRAMES-eng tag
	mkvTagJSON := []byte(`{
		"streams": [
			{
				"codec_type": "video",
				"codec_name": "h264",
				"width": 1280,
				"height": 720,
				"r_frame_rate": "24/1",
				"tags": {"NUMBER_OF_FRAMES-eng": "2400"}
			}
		],
		"format": {"duration": "100.0"}
	}`)

	// Act
	metaTag, err := probe.ParseFFprobeJSON(mkvTagJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert
	if metaTag.TotalFrames != 2400 {
		t.Fatalf("expected 2400 frames from MKV tag, got %d", metaTag.TotalFrames)
	}

	// Arrange: fallback to duration * fps
	durationJSON := []byte(`{
		"streams": [
			{
				"codec_type": "video",
				"codec_name": "h264",
				"width": 1280,
				"height": 720,
				"r_frame_rate": "30"
			}
		],
		"format": {"duration": "10.0"}
	}`)

	// Act
	metaDur, err := probe.ParseFFprobeJSON(durationJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Assert
	if metaDur.TotalFrames != 300 {
		t.Fatalf("expected 300 frames from duration fallback, got %d", metaDur.TotalFrames)
	}
}

func TestProbeFileAndRejectsInvalidInputs(t *testing.T) {
	// Arrange
	runner := &FakeProbeRunner{
		Output: `{"streams":[{"codec_type":"video","width":1920,"height":1080,"r_frame_rate":"24/1","nb_frames":"240"}],"format":{"duration":"10.0"}}`,
	}

	// Act
	meta, err := probe.ProbeFile(context.Background(), runner, "/usr/bin/ffprobe", "/videos/ep1.mkv")

	// Assert
	if err != nil || meta.TotalFrames != 240 {
		t.Fatalf("unexpected ProbeFile result: meta=%+v err=%v", meta, err)
	}
	if runner.CapturedName != "/usr/bin/ffprobe" {
		t.Fatalf("expected /usr/bin/ffprobe, got %q", runner.CapturedName)
	}

	// Error cases
	if _, err := probe.ParseFFprobeJSON([]byte(`{invalid`)); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
	if _, err := probe.ParseFFprobeJSON([]byte(`{"streams":[{"codec_type":"audio"}]}`)); err == nil {
		t.Fatal("expected error when no video stream is present")
	}
	if _, err := probe.ParseFFprobeJSON([]byte(`{"streams":[{"codec_type":"video","width":1920,"height":1080,"r_frame_rate":"24/0"}]}`)); err == nil {
		t.Fatal("expected error when r_frame_rate has zero denominator")
	}

	runner.Err = errors.New("exit status 1")
	if _, err := probe.ProbeFile(context.Background(), runner, "ffprobe", "/videos/missing.mkv"); err == nil {
		t.Fatal("expected error when ffprobe command fails")
	}
}
