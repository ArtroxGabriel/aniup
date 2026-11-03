package state

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gabrigas/aniup/internal/config"
)

// ChunkStatus represents the processing state of an individual video chunk.
type ChunkStatus string

const (
	ChunkPending   ChunkStatus = "pending"
	ChunkCompleted ChunkStatus = "completed"

	ManifestFileName     = "manifest.json"
	PartialHashWindowLen = int64(4 * 1024 * 1024) // 4 MiB head + 4 MiB tail
)

// ChunkRecord stores the frame range and completion metadata for a single chunk.
type ChunkRecord struct {
	Index         int         `json:"index"`
	StartFrame    int         `json:"start_frame"`
	EndFrame      int         `json:"end_frame"`
	Status        ChunkStatus `json:"status"`
	SegmentFile   string      `json:"segment_file"`
	SHA256Segment string      `json:"sha256_segment,omitempty"`
}

// Manifest persists the checkpoint state of an upscale job inside .aniup_<filename>_<scale>x/manifest.json.
type Manifest struct {
	InputPath   string        `json:"input_path"`
	InputSHA256 string        `json:"input_sha256"`
	Engine      string        `json:"engine"`
	Model       string        `json:"model"`
	Scale       int           `json:"scale"`
	Encoder     string        `json:"encoder"`
	ChunkSize   int           `json:"chunk_size"`
	TotalFrames int           `json:"total_frames"`
	FrameRate   string        `json:"frame_rate"`
	Chunks      []ChunkRecord `json:"chunks"`
}

// WorkDirPath returns the hidden checkpoint directory path alongside the input video.
// Example: dir := state.WorkDirPath("/videos/ep01.mkv", 2)
func WorkDirPath(inputPath string, scale int) string {
	dir := filepath.Dir(inputPath)
	base := filepath.Base(inputPath)
	return filepath.Join(dir, fmt.Sprintf(".aniup_%s_%dx", base, scale))
}

// ComputePartialSHA256 hashes the file size plus the first and last 4 MiB of filePath.
// Example: sum, err := state.ComputePartialSHA256("/videos/ep01.mkv")
func ComputePartialSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open %q for partial hash: %w", filePath, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat %q: %w", filePath, err)
	}
	size := info.Size()
	hasher := sha256.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(size))
	_, _ = hasher.Write(sizeBuf[:])

	if size <= PartialHashWindowLen*2 {
		if _, err := io.Copy(hasher, f); err != nil {
			return "", fmt.Errorf("failed to read %q: %w", filePath, err)
		}
		return hex.EncodeToString(hasher.Sum(nil)), nil
	}
	return hashHeadAndTail(f, hasher, size)
}

func hashHeadAndTail(f *os.File, hasher io.Writer, size int64) (string, error) {
	if _, err := io.CopyN(hasher, f, PartialHashWindowLen); err != nil {
		return "", fmt.Errorf("failed to read head bytes: %w", err)
	}
	if _, err := f.Seek(size-PartialHashWindowLen, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek tail bytes: %w", err)
	}
	if _, err := io.CopyN(hasher, f, PartialHashWindowLen); err != nil {
		return "", fmt.Errorf("failed to read tail bytes: %w", err)
	}
	if sum, ok := hasher.(interface{ Sum([]byte) []byte }); ok {
		return hex.EncodeToString(sum.Sum(nil)), nil
	}
	return "", fmt.Errorf("unexpected hasher type")
}

// ComputeFileSHA256 computes the full SHA256 hex digest of a file (such as an encoded chunk segment).
// Example: sum, err := state.ComputeFileSHA256(segPath)
func ComputeFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open %q for SHA256: %w", filePath, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash %q: %w", filePath, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NewManifest constructs a fresh Manifest partitioned into chunks of cfg.ChunkSize frames.
// Example: m := state.NewManifest(cfg, inputHash, 1440, "24000/1001")
func NewManifest(cfg config.RunConfig, inputSHA256 string, totalFrames int, frameRate string) *Manifest {
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = config.DefaultChunkSize
	}
	if totalFrames <= 0 {
		totalFrames = 1
	}
	var chunks []ChunkRecord
	for start, idx := 0, 0; start < totalFrames; start, idx = start+chunkSize, idx+1 {
		end := start + chunkSize - 1
		if end >= totalFrames {
			end = totalFrames - 1
		}
		chunks = append(chunks, ChunkRecord{
			Index:       idx,
			StartFrame:  start,
			EndFrame:    end,
			Status:      ChunkPending,
			SegmentFile: fmt.Sprintf("seg_%05d.mkv", idx+1),
		})
	}
	return &Manifest{
		InputPath:   cfg.InputPath,
		InputSHA256: inputSHA256,
		Engine:      string(cfg.Engine),
		Model:       cfg.Model,
		Scale:       cfg.Scale,
		Encoder:     string(cfg.Encoder),
		ChunkSize:   chunkSize,
		TotalFrames: totalFrames,
		FrameRate:   frameRate,
		Chunks:      chunks,
	}
}

// SaveManifest atomically writes the manifest to <workDir>/manifest.json via a temporary file rename.
// Example: err := state.SaveManifest(workDir, manifest)
func SaveManifest(workDir string, m *Manifest) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("failed to create workDir %q: %w", workDir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	tmpPath := filepath.Join(workDir, ManifestFileName+".tmp")
	finalPath := filepath.Join(workDir, ManifestFileName)
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary manifest %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("failed to rename manifest %q -> %q: %w", tmpPath, finalPath, err)
	}
	return nil
}

// LoadManifest reads and parses <workDir>/manifest.json.
// Example: m, err := state.LoadManifest(workDir)
func LoadManifest(workDir string) (*Manifest, error) {
	finalPath := filepath.Join(workDir, ManifestFileName)
	data, err := os.ReadFile(finalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest %q: %w", finalPath, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest %q: %w", finalPath, err)
	}
	return &m, nil
}

// IsCompatible verifies whether an existing Manifest matches the current input video hash and upscale settings.
// Example: ok := state.IsCompatible(manifest, inputHash, cfg)
func IsCompatible(m *Manifest, inputSHA256 string, cfg config.RunConfig) bool {
	if m == nil {
		return false
	}
	return m.InputSHA256 == inputSHA256 &&
		m.Engine == string(cfg.Engine) &&
		m.Model == cfg.Model &&
		m.Scale == cfg.Scale &&
		m.Encoder == string(cfg.Encoder) &&
		m.ChunkSize == cfg.ChunkSize
}

// FindExistingManifest checks whether a valid checkpoint manifest with completed chunks exists for inputPath (2x or 4x).
// Example: workDir, m, ok := state.FindExistingManifest("/videos/ep01.mkv")
func FindExistingManifest(inputPath string) (string, *Manifest, bool) {
	if inputPath == "" {
		return "", nil, false
	}
	inputHash, err := ComputePartialSHA256(inputPath)
	if err != nil {
		return "", nil, false
	}
	for _, scale := range []int{config.DefaultScale, config.AlternateScale} {
		workDir := WorkDirPath(inputPath, scale)
		m, loadErr := LoadManifest(workDir)
		if loadErr == nil && m.InputSHA256 == inputHash && m.CompletedCount() > 0 {
			return workDir, m, true
		}
	}
	return "", nil, false
}

// DropCheckpoints removes any existing .aniup_<filename>_<scale>x checkpoint directories for inputPath.
// Example: removed, err := state.DropCheckpoints("/videos/ep01.mkv")
func DropCheckpoints(inputPath string) ([]string, error) {
	if inputPath == "" {
		return nil, fmt.Errorf("invalid empty inputPath: expected video path to drop checkpoints")
	}
	var removed []string
	for _, scale := range []int{config.DefaultScale, config.AlternateScale} {
		workDir := WorkDirPath(inputPath, scale)
		if _, err := os.Stat(workDir); err == nil {
			if rmErr := os.RemoveAll(workDir); rmErr != nil {
				return removed, fmt.Errorf("failed to remove checkpoint directory %q: %w", workDir, rmErr)
			}
			removed = append(removed, workDir)
		}
	}
	return removed, nil
}

// CompletedCount returns the number of chunks marked as completed.
// Example: done := manifest.CompletedCount()
func (m *Manifest) CompletedCount() int {
	count := 0
	for _, c := range m.Chunks {
		if c.Status == ChunkCompleted {
			count++
		}
	}
	return count
}

// MarkChunkCompleted returns a new Manifest copy with the specified chunk marked completed and persists it atomically.
// Example: updated, err := state.MarkChunkCompleted(workDir, manifest, 0, "seg_00001.mkv", segHash)
func MarkChunkCompleted(workDir string, m *Manifest, index int, segFile, segHash string) (*Manifest, error) {
	if index < 0 || index >= len(m.Chunks) {
		return nil, fmt.Errorf("chunk index %d out of range [0, %d)", index, len(m.Chunks))
	}
	updatedChunks := make([]ChunkRecord, len(m.Chunks))
	copy(updatedChunks, m.Chunks)
	updatedChunks[index] = ChunkRecord{
		Index:         updatedChunks[index].Index,
		StartFrame:    updatedChunks[index].StartFrame,
		EndFrame:      updatedChunks[index].EndFrame,
		Status:        ChunkCompleted,
		SegmentFile:   segFile,
		SHA256Segment: segHash,
	}
	updated := *m
	updated.Chunks = updatedChunks
	if err := SaveManifest(workDir, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}
