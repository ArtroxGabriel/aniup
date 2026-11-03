package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/gabrigas/aniup/internal/config"
)

// EngineModelOption represents a selectable (Engine, Model) pair in the Huh interactive wizard.
type EngineModelOption struct {
	Key    string
	Label  string
	Engine config.EngineType
	Model  string
}

// BuildEngineModelOptions returns the selectable engine/model pairs filtered by the binaries installed on the system.
// Example: opts := tui.BuildEngineModelOptions(report.AvailableEngines())
func BuildEngineModelOptions(availableEngines []config.EngineType) []EngineModelOption {
	var opts []EngineModelOption
	for _, eng := range availableEngines {
		for _, model := range config.ModelsForEngine(eng) {
			key := fmt.Sprintf("%s|%s", eng, model)
			label := fmt.Sprintf("%s (%s)", eng, model)
			opts = append(opts, EngineModelOption{
				Key:    key,
				Label:  label,
				Engine: eng,
				Model:  model,
			})
		}
	}
	return opts
}

// ParseEngineModelSelection splits a combined "engine|model" wizard key back into typed values.
// Example: eng, model, err := tui.ParseEngineModelSelection("realesrgan-ncnn-vulkan|realesr-animevideov3")
func ParseEngineModelSelection(key string) (config.EngineType, string, error) {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid engine/model selection %q: expected 'engine|model'", key)
	}
	return config.EngineType(parts[0]), parts[1], nil
}

// RunWizard opens the interactive Huh form to collect upscale parameters from the user.
// Example: cfg, err := tui.RunWizard(initialCfg, availableEngines, freeShmBytes)
func RunWizard(
	initial config.RunConfig,
	availableEngines []config.EngineType,
	freeShmBytes uint64,
) (config.RunConfig, error) {
	opts := BuildEngineModelOptions(availableEngines)
	if len(opts) == 0 {
		return config.RunConfig{}, fmt.Errorf("no NCNN engines available for wizard selection")
	}

	inputPath := initial.InputPath
	engineModelKey := opts[0].Key
	scale := config.DefaultScale
	profile := string(config.ProfileTurbo)
	chunkSizeStr := strconv.Itoa(config.DefaultChunkSize)
	encoder := string(config.EncoderHEVCNVENC)

	form := BuildWizardForm(&inputPath, &engineModelKey, &scale, &profile, &chunkSizeStr, &encoder, opts, freeShmBytes)
	if err := form.Run(); err != nil {
		return config.RunConfig{}, err
	}
	return AssembleWizardConfig(initial, inputPath, engineModelKey, scale, profile, chunkSizeStr, encoder, freeShmBytes)
}

// PromptResumeCheckpoint asks the user via Huh whether to resume from an existing compatible manifest.json.
// Example: resume, err := tui.PromptResumeCheckpoint(14, 48)
func PromptResumeCheckpoint(completedChunks, totalChunks int) (bool, error) {
	resume := true
	title := fmt.Sprintf("Checkpoint detectado (%d/%d chunks concluídos). Deseja retomar de onde parou?", completedChunks, totalChunks)
	confirm := huh.NewConfirm().
		Title(title).
		Affirmative("Sim, retomar").
		Negative("Não, recomeçar do zero").
		Value(&resume)
	if err := huh.NewForm(huh.NewGroup(confirm)).Run(); err != nil {
		return false, err
	}
	return resume, nil
}

// BuildWizardForm constructs the interactive Huh form for the given state pointers and options.
func BuildWizardForm(
	inputPath *string,
	engineModelKey *string,
	scale *int,
	profile *string,
	chunkSizeStr *string,
	encoder *string,
	opts []EngineModelOption,
	freeShmBytes uint64,
) *huh.Form {
	var fields []huh.Field
	if strings.TrimSpace(*inputPath) == "" {
		fields = append(fields, huh.NewFilePicker().
			Title("1. Arquivo de vídeo de entrada").
			AllowedTypes([]string{".mkv", ".mp4", ".webm", ".avi"}).
			Value(inputPath))
	}
	huhOpts := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		huhOpts = append(huhOpts, huh.NewOption(o.Label, o.Key))
	}
	shmGiB := float64(freeShmBytes) / (1024 * 1024 * 1024)
	fields = append(fields,
		huh.NewSelect[string]().Title("2. Engine & Modelo NCNN").Options(huhOpts...).Value(engineModelKey),
		huh.NewSelect[int]().Title("3. Escala de Upscale").Options(
			huh.NewOption("2x (Padrão Recomendado)", config.DefaultScale),
			huh.NewOption("4x (Ultra Resolução)", config.AlternateScale),
		).Value(scale),
		huh.NewSelect[string]().Title("4. Perfil de VRAM / Tiling").Options(
			huh.NewOption("Turbo (dGPU dedicada livre 6GB - Tile 768, Threads 2:3:3)", string(config.ProfileTurbo)),
			huh.NewOption("Balanceado (Tile 400, Threads 1:2:2)", string(config.ProfileBalanced)),
			huh.NewOption("Conservador (Tile 256, Threads 1:1:1)", string(config.ProfileConservative)),
		).Value(profile),
		huh.NewInput().
			Title(fmt.Sprintf("5. Tamanho do Chunk em frames (/dev/shm livre: %.2f GiB)", shmGiB)).
			Description("600 frames consomem ~1.5 GiB em 1080p->2x no /dev/shm").
			Value(chunkSizeStr),
		huh.NewSelect[string]().Title("6. Encoder de Saída NVENC").Options(
			huh.NewOption("hevc_nvenc (CQ 19, preset p6 - Padrão)", string(config.EncoderHEVCNVENC)),
			huh.NewOption("av1_nvenc (CQ 19, preset p6)", string(config.EncoderAV1NVENC)),
		).Value(encoder),
	)
	return huh.NewForm(huh.NewGroup(fields...))
}

// AssembleWizardConfig validates and converts raw Huh form values into a RunConfig.
func AssembleWizardConfig(
	initial config.RunConfig,
	inputPath, engineModelKey string,
	scale int,
	profile, chunkSizeStr, encoder string,
	freeShmBytes uint64,
) (config.RunConfig, error) {
	eng, model, err := ParseEngineModelSelection(engineModelKey)
	if err != nil {
		return config.RunConfig{}, err
	}
	chunkSize, err := strconv.Atoi(strings.TrimSpace(chunkSizeStr))
	if err != nil || chunkSize <= 0 {
		return config.RunConfig{}, fmt.Errorf("invalid chunk size %q: expected positive integer", chunkSizeStr)
	}
	outPath := initial.OutputPath
	if strings.TrimSpace(outPath) == "" {
		outPath = config.DefaultOutputPath(inputPath, scale, model)
	}
	cfg := config.RunConfig{
		InputPath:   inputPath,
		OutputPath:  outPath,
		Engine:      eng,
		Model:       model,
		Scale:       scale,
		Profile:     config.VRAMProfile(profile),
		ChunkSize:   chunkSize,
		Encoder:     config.EncoderType(encoder),
		KeepChunks:  initial.KeepChunks,
		InputWidth:  initial.InputWidth,
		InputHeight: initial.InputHeight,
	}
	if err := config.ValidateConfig(cfg, freeShmBytes); err != nil {
		return config.RunConfig{}, err
	}
	return cfg, nil
}
