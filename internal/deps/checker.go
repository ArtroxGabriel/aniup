package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/gabrigas/aniup/internal/config"
)

const (
	DefaultShmPath  = "/dev/shm"
	MinShmFreeBytes = uint64(4) * 1024 * 1024 * 1024 // 4 GiB

	FFmpegInstallCmd     = "sudo pacman -S ffmpeg"
	NvidiaInstallCmd     = "sudo pacman -S nvidia-utils"
	RealESRGANInstallCmd = "paru -S realesrgan-ncnn-vulkan-bin (ou yay -S realesrgan-ncnn-vulkan-bin)"
	RealCUGANInstallCmd  = "paru -S realcugan-ncnn-vulkan-bin (ou yay -S realcugan-ncnn-vulkan-bin)"

	FFmpegFallbackURL     = "https://archlinux.org/packages/extra/x86_64/ffmpeg/"
	NvidiaFallbackURL     = "https://wiki.archlinux.org/title/NVIDIA"
	RealESRGANFallbackURL = "https://github.com/xinntao/Real-ESRGAN-ncnn-vulkan/releases"
	RealCUGANFallbackURL  = "https://github.com/nihui/realcugan-ncnn-vulkan/releases"
)

// SystemInspector abstracts OS and subprocess probing for testability.
type SystemInspector interface {
	LookPath(file string) (string, error)
	RunCommand(ctx context.Context, name string, args ...string) (string, error)
	ShmStatus(path string) (freeBytes uint64, writable bool, err error)
}

// OSSystemInspector implements SystemInspector against the real host OS.
type OSSystemInspector struct{}

// NewOSSystemInspector returns a production SystemInspector.
// Example: inspector := deps.NewOSSystemInspector()
func NewOSSystemInspector() *OSSystemInspector {
	return &OSSystemInspector{}
}

// LookPath searches for an executable named file in the directories named by the PATH environment variable.
// Example: path, err := inspector.LookPath("ffmpeg")
func (o *OSSystemInspector) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// RunCommand executes a binary with explicit args and returns combined stdout/stderr.
// Example: out, err := inspector.RunCommand(ctx, "ffmpeg", "-hide_banner", "-encoders")
func (o *OSSystemInspector) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ShmStatus inspects free bytes and write permissions on the given tmpfs directory.
// Example: free, writable, err := inspector.ShmStatus("/dev/shm")
func (o *OSSystemInspector) ShmStatus(path string) (uint64, bool, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, false, fmt.Errorf("statfs failed for %q: %w", path, err)
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	tmpFile, err := os.CreateTemp(path, ".aniup_probe_*")
	if err != nil {
		return freeBytes, false, nil
	}
	_ = tmpFile.Close()
	_ = os.Remove(tmpFile.Name())
	return freeBytes, true, nil
}

// MissingDep describes a missing or misconfigured system requirement and how to fix it on Arch/CachyOS.
type MissingDep struct {
	Name           string
	Reason         string
	InstallCommand string
	FallbackURL    string
}

// CheckReport aggregates the results of all Phase 0 environment checks.
type CheckReport struct {
	FFmpegPath       string
	FFprobePath      string
	RealESRGANPath   string
	RealCUGANPath    string
	NvidiaSMIPath    string
	HasHEVCNVENC     bool
	HasAV1NVENC      bool
	ShmPath          string
	ShmWritable      bool
	ShmFreeBytes     uint64
	MissingMandatory []MissingDep
}

// IsOK returns true when all mandatory dependencies and capabilities are satisfied.
// Example: if !report.IsOK() { os.Exit(1) }
func (r CheckReport) IsOK() bool {
	return len(r.MissingMandatory) == 0
}

// AvailableEngines returns the list of installed NCNN engines detected in $PATH.
// Example: engines := report.AvailableEngines()
func (r CheckReport) AvailableEngines() []config.EngineType {
	var engines []config.EngineType
	if r.RealESRGANPath != "" {
		engines = append(engines, config.EngineRealESRGAN)
	}
	if r.RealCUGANPath != "" {
		engines = append(engines, config.EngineRealCUGAN)
	}
	return engines
}

// CheckAll runs all binary, NVENC encoder, and /dev/shm checks using the given SystemInspector.
// Example: report := deps.CheckAll(ctx, deps.NewOSSystemInspector(), deps.DefaultShmPath)
func CheckAll(ctx context.Context, inspector SystemInspector, shmPath string) CheckReport {
	if shmPath == "" {
		shmPath = DefaultShmPath
	}
	report := CheckReport{ShmPath: shmPath}
	report.FFmpegPath, _ = inspector.LookPath("ffmpeg")
	report.FFprobePath, _ = inspector.LookPath("ffprobe")
	report.RealESRGANPath, _ = inspector.LookPath(string(config.EngineRealESRGAN))
	report.RealCUGANPath, _ = inspector.LookPath(string(config.EngineRealCUGAN))
	report.NvidiaSMIPath, _ = inspector.LookPath("nvidia-smi")

	report.MissingMandatory = append(report.MissingMandatory, checkFFmpegSuite(ctx, inspector, &report)...)
	report.MissingMandatory = append(report.MissingMandatory, checkNCNNEngines(report)...)
	report.MissingMandatory = append(report.MissingMandatory, checkShmDirectory(inspector, &report)...)
	return report
}

func checkFFmpegSuite(ctx context.Context, inspector SystemInspector, report *CheckReport) []MissingDep {
	var missing []MissingDep
	if report.FFmpegPath == "" || report.FFprobePath == "" {
		missing = append(missing, MissingDep{
			Name:           "ffmpeg / ffprobe",
			Reason:         "Binários obrigatórios para extração, probe e codificação não encontrados no $PATH",
			InstallCommand: FFmpegInstallCmd,
			FallbackURL:    FFmpegFallbackURL,
		})
		return append(missing, MissingDep{
			Name:           "hevc_nvenc / av1_nvenc",
			Reason:         "Não foi possível validar encoders NVENC sem o ffmpeg instalado",
			InstallCommand: NvidiaInstallCmd,
			FallbackURL:    NvidiaFallbackURL,
		})
	}
	out, err := inspector.RunCommand(ctx, report.FFmpegPath, "-hide_banner", "-encoders")
	report.HasHEVCNVENC = err == nil && strings.Contains(out, "hevc_nvenc")
	report.HasAV1NVENC = err == nil && strings.Contains(out, "av1_nvenc")
	if !report.HasHEVCNVENC || !report.HasAV1NVENC {
		missing = append(missing, MissingDep{
			Name:           "hevc_nvenc / av1_nvenc",
			Reason:         "Encoders NVIDIA NVENC ausentes no ffmpeg (verifique drivers NVIDIA ativos)",
			InstallCommand: NvidiaInstallCmd,
			FallbackURL:    NvidiaFallbackURL,
		})
	}
	return missing
}

func checkNCNNEngines(report CheckReport) []MissingDep {
	if report.RealESRGANPath != "" || report.RealCUGANPath != "" {
		return nil
	}
	return []MissingDep{
		{
			Name:           string(config.EngineRealESRGAN),
			Reason:         "Nenhum motor de upscale NCNN Vulkan encontrado no $PATH (instale pelo menos um)",
			InstallCommand: RealESRGANInstallCmd,
			FallbackURL:    RealESRGANFallbackURL,
		},
		{
			Name:           string(config.EngineRealCUGAN),
			Reason:         "Motor alternativo NCNN Vulkan ausente no $PATH",
			InstallCommand: RealCUGANInstallCmd,
			FallbackURL:    RealCUGANFallbackURL,
		},
	}
}

func checkShmDirectory(inspector SystemInspector, report *CheckReport) []MissingDep {
	freeBytes, writable, err := inspector.ShmStatus(report.ShmPath)
	report.ShmFreeBytes = freeBytes
	report.ShmWritable = writable
	if err != nil || !writable {
		return []MissingDep{{
			Name:           report.ShmPath,
			Reason:         "Diretório RAM disk (/dev/shm) inexistente ou sem permissão de escrita",
			InstallCommand: "sudo mount -o remount,rw,size=8G /dev/shm",
			FallbackURL:    "https://wiki.archlinux.org/title/Tmpfs",
		}}
	}
	if freeBytes < MinShmFreeBytes {
		return []MissingDep{{
			Name:           report.ShmPath,
			Reason:         fmt.Sprintf("Espaço livre insuficiente em %s: %.2f GiB livres (mínimo 4.00 GiB)", report.ShmPath, float64(freeBytes)/(1024*1024*1024)),
			InstallCommand: "sudo mount -o remount,size=8G /dev/shm",
			FallbackURL:    "https://wiki.archlinux.org/title/Tmpfs",
		}}
	}
	return nil
}
