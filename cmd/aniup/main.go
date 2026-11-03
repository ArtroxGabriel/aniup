package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gabrigas/aniup/internal/config"
	"github.com/gabrigas/aniup/internal/deps"
	"github.com/gabrigas/aniup/internal/pipeline"
	"github.com/gabrigas/aniup/internal/probe"
	"github.com/gabrigas/aniup/internal/state"
	"github.com/gabrigas/aniup/internal/tui"
)

// CLIFlags holds parsed command-line options before Wizard resolution.
type CLIFlags struct {
	InputPath  string
	OutputPath string
	Engine     string
	Model      string
	Scale      int
	Profile    string
	ChunkSize  int
	Encoder    string
	Resume     bool
	KeepChunks bool
	ShowHelp   bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runApp(ctx, stop, os.Args[1:], deps.NewOSSystemInspector(), pipeline.NewOSProcessExecutor(), os.Stdout, false))
}

func runApp(
	ctx context.Context,
	cancel context.CancelFunc,
	args []string,
	inspector deps.SystemInspector,
	procExec pipeline.ProcessExecutor,
	out io.Writer,
	headless bool,
) int {
	flags, isDoctor, err := ParseCLIArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Erro nos argumentos: %v\n\n", err)
		PrintHelp(out)
		return 2
	}
	if flags.ShowHelp {
		PrintHelp(out)
		return 0
	}
	if isDoctor {
		return RunDoctor(ctx, inspector, out)
	}
	if err := executeMainFlow(ctx, cancel, inspector, procExec, flags, out, headless); err != nil {
		_, _ = fmt.Fprintf(out, "Erro: %v\n", err)
		return 1
	}
	return 0
}

// ParseCLIArgs parses command-line arguments and detects the `doctor` or `help` subcommands and `--help`/`-h` flags.
// Example: flags, isDoctor, err := ParseCLIArgs(os.Args[1:])
func ParseCLIArgs(args []string) (CLIFlags, bool, error) {
	if len(args) > 0 && args[0] == "doctor" {
		return CLIFlags{}, true, nil
	}
	if len(args) > 0 && args[0] == "help" {
		return CLIFlags{ShowHelp: true}, false, nil
	}
	fs := flag.NewFlagSet("aniup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var f CLIFlags
	fs.BoolVar(&f.ShowHelp, "h", false, "Exibir ajuda de uso e sair")
	fs.BoolVar(&f.ShowHelp, "help", false, "Exibir ajuda de uso e sair")
	fs.StringVar(&f.InputPath, "i", "", "Arquivo de vídeo de entrada")
	fs.StringVar(&f.InputPath, "input", "", "Arquivo de vídeo de entrada")
	fs.StringVar(&f.OutputPath, "o", "", "Arquivo de vídeo de saída")
	fs.StringVar(&f.OutputPath, "output", "", "Arquivo de vídeo de saída")
	fs.StringVar(&f.Engine, "engine", "", "Motor NCNN (realesrgan-ncnn-vulkan ou realcugan-ncnn-vulkan)")
	fs.StringVar(&f.Model, "model", "", "Modelo NCNN (ex: realesr-animevideov3, models-se)")
	fs.IntVar(&f.Scale, "scale", 0, "Escala de upscale (2 ou 4)")
	fs.StringVar(&f.Profile, "profile", "", "Perfil de VRAM (turbo, balanced, conservative)")
	fs.IntVar(&f.ChunkSize, "chunk-size", 0, "Tamanho do chunk em frames (padrão 600)")
	fs.StringVar(&f.Encoder, "encoder", "", "Encoder NVENC (hevc_nvenc ou av1_nvenc)")
	fs.BoolVar(&f.Resume, "resume", false, "Retomar automaticamente de checkpoint existente")
	fs.BoolVar(&f.KeepChunks, "keep-chunks", false, "Manter diretório oculto de chunks após concatenação")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return CLIFlags{ShowHelp: true}, false, nil
		}
		return CLIFlags{}, false, err
	}
	if f.InputPath == "" && fs.NArg() > 0 {
		f.InputPath = fs.Arg(0)
	}
	return f, false, nil
}

// PrintHelp writes the formatted CLI usage, subcommands, and flags reference to out.
// Example: PrintHelp(os.Stdout)
func PrintHelp(out io.Writer) {
	helpText := `aniup — Chunked Anime Video Upscaler em RAM (/dev/shm) com NCNN Vulkan & NVENC

USO:
  aniup [flags] [video.mkv]
  aniup doctor
  aniup --help

SUBCOMANDOS:
  doctor              Valida binários (ffmpeg, ffprobe, motores NCNN), encoders NVENC e espaço em /dev/shm

FLAGS:
  -i, --input string      Arquivo de vídeo de entrada (ou via argumento posicional)
  -o, --output string     Arquivo de vídeo de saída (padrão: <nome>_<scale>x_<model>.mkv)
      --engine string     Motor NCNN: realesrgan-ncnn-vulkan | realcugan-ncnn-vulkan
      --model string      Modelo NCNN: realesr-animevideov3 | realesrgan-x4plus-anime | models-se | models-pro
      --scale int         Escala de upscale: 2 (padrão) ou 4
      --profile string    Perfil de VRAM/Tiling: turbo (768/2:3:3) | balanced (400/1:2:2) | conservative (256/1:1:1)
      --chunk-size int    Tamanho do chunk em frames no /dev/shm (padrão: 600)
      --encoder string    Encoder NVIDIA NVENC: hevc_nvenc (padrão, CQ 19 p6) | av1_nvenc
      --resume            Retomar automaticamente de checkpoint existente (.aniup_<video>_<scale>x)
      --keep-chunks       Manter diretório oculto de chunks após a concatenação final
  -h, --help              Exibir esta mensagem de ajuda e sair

EXEMPLOS:
  # Abrir o Wizard interativo (Huh):
  aniup episodio01.mkv

  # Execução direta via CLI sem Wizard:
  aniup -i episodio01.mkv --engine realesrgan-ncnn-vulkan --model realesr-animevideov3 --scale 2 --profile turbo --encoder hevc_nvenc
`
	_, _ = fmt.Fprint(out, helpText)
}

// NeedsWizard returns true when required upscale flags were omitted on the CLI.
// Example: if NeedsWizard(flags) { ... }
func NeedsWizard(f CLIFlags) bool {
	return strings.TrimSpace(f.InputPath) == "" ||
		strings.TrimSpace(f.Engine) == "" ||
		strings.TrimSpace(f.Model) == "" ||
		f.Scale == 0 ||
		strings.TrimSpace(f.Profile) == "" ||
		strings.TrimSpace(f.Encoder) == ""
}

// RunDoctor executes Phase 0 diagnostics, writes the styled Lipgloss report to out, and returns exit code 0 or 1.
// Example: code := RunDoctor(ctx, inspector, os.Stdout)
func RunDoctor(ctx context.Context, inspector deps.SystemInspector, out io.Writer) int {
	report := deps.CheckAll(ctx, inspector, deps.DefaultShmPath)
	_, _ = fmt.Fprintln(out, tui.RenderDoctorReport(report))
	if !report.IsOK() {
		return 1
	}
	return 0
}

func executeMainFlow(
	ctx context.Context,
	cancel context.CancelFunc,
	inspector deps.SystemInspector,
	procExec pipeline.ProcessExecutor,
	flags CLIFlags,
	out io.Writer,
	headless bool,
) error {
	report := deps.CheckAll(ctx, inspector, deps.DefaultShmPath)
	if !report.IsOK() {
		_, _ = fmt.Fprintln(out, tui.RenderDoctorReport(report))
		return fmt.Errorf("dependências obrigatórias ausentes")
	}

	cfg, meta, err := resolveConfigAndProbe(ctx, inspector, report, flags)
	if err != nil {
		return err
	}
	workDir, manifest, err := prepareCheckpointManifest(cfg, meta, NeedsWizard(flags))
	if err != nil {
		return err
	}
	return runPipelineWithDashboard(ctx, cancel, report, procExec, cfg, meta, workDir, manifest, headless)
}

func resolveConfigAndProbe(
	ctx context.Context,
	inspector deps.SystemInspector,
	report deps.CheckReport,
	flags CLIFlags,
) (config.RunConfig, probe.VideoMetadata, error) {
	chunkSize := flags.ChunkSize
	if chunkSize <= 0 {
		chunkSize = config.DefaultChunkSize
	}
	initial := config.RunConfig{
		InputPath:  flags.InputPath,
		OutputPath: flags.OutputPath,
		Engine:     config.EngineType(flags.Engine),
		Model:      flags.Model,
		Scale:      flags.Scale,
		Profile:    config.VRAMProfile(flags.Profile),
		ChunkSize:  chunkSize,
		Encoder:    config.EncoderType(flags.Encoder),
		Resume:     flags.Resume,
		KeepChunks: flags.KeepChunks,
	}
	if NeedsWizard(flags) {
		wizCfg, err := tui.RunWizard(initial, report.AvailableEngines(), report.ShmFreeBytes)
		if err != nil {
			return config.RunConfig{}, probe.VideoMetadata{}, err
		}
		initial = wizCfg
	}
	meta, err := probe.ProbeFile(ctx, inspector, report.FFprobePath, initial.InputPath)
	if err != nil {
		return config.RunConfig{}, probe.VideoMetadata{}, err
	}
	initial.InputWidth = meta.Width
	initial.InputHeight = meta.Height
	if initial.OutputPath == "" {
		initial.OutputPath = config.DefaultOutputPath(initial.InputPath, initial.Scale, initial.Model)
	}
	if err := config.ValidateConfig(initial, report.ShmFreeBytes); err != nil {
		return config.RunConfig{}, probe.VideoMetadata{}, err
	}
	return initial, meta, nil
}

func prepareCheckpointManifest(
	cfg config.RunConfig,
	meta probe.VideoMetadata,
	interactive bool,
) (string, *state.Manifest, error) {
	workDir := state.WorkDirPath(cfg.InputPath, cfg.Scale)
	inputHash, err := state.ComputePartialSHA256(cfg.InputPath)
	if err != nil {
		return "", nil, err
	}
	if existing, loadErr := state.LoadManifest(workDir); loadErr == nil {
		if state.IsCompatible(existing, inputHash, cfg) && existing.CompletedCount() > 0 {
			resume := cfg.Resume
			if interactive && !resume {
				resume, err = tui.PromptResumeCheckpoint(existing.CompletedCount(), len(existing.Chunks))
				if err != nil {
					return "", nil, err
				}
			}
			if resume {
				return workDir, existing, nil
			}
		}
	}
	fresh := state.NewManifest(cfg, inputHash, meta.TotalFrames, meta.FrameRateRatio)
	if err := state.SaveManifest(workDir, fresh); err != nil {
		return "", nil, err
	}
	return workDir, fresh, nil
}

func runPipelineWithDashboard(
	ctx context.Context,
	cancel context.CancelFunc,
	report deps.CheckReport,
	procExec pipeline.ProcessExecutor,
	cfg config.RunConfig,
	meta probe.VideoMetadata,
	workDir string,
	manifest *state.Manifest,
	headless bool,
) error {
	enginePath := report.RealESRGANPath
	if cfg.Engine == config.EngineRealCUGAN {
		enginePath = report.RealCUGANPath
	}
	events := make(chan pipeline.Event, 64)
	runner := pipeline.NewRunner(procExec, report.ShmPath, report.FFmpegPath, enginePath)

	errCh := make(chan error, 1)
	go func() {
		defer close(events)
		errCh <- runner.Run(ctx, cfg, workDir, manifest, events)
	}()

	telem := &tui.OSTelemetryReader{NvidiaSMIPath: report.NvidiaSMIPath}
	dash := tui.NewDashboardModel(ctx, cancel, cfg, meta, events, telem)
	var opts []tea.ProgramOption
	if headless {
		opts = append(opts, tea.WithInput(nil), tea.WithoutRenderer())
	}
	prog := tea.NewProgram(dash, opts...)
	_, _ = prog.Run()
	return <-errCh
}
