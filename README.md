# `aniup` — Chunked Anime Video Upscaler em RAM (`/dev/shm`)

`aniup` é uma ferramenta CLI/TUI escrita em Go (binário único estático) para upscaling local de vídeos de anime e animação utilizando `ffmpeg`, `ffprobe` e motores NCNN Vulkan (`realesrgan-ncnn-vulkan` e `realcugan-ncnn-vulkan`).

O foco principal da arquitetura é **zero desgaste de SSD** (extração e upscale de frames inteiramente em RAM via `/dev/shm`), **controle estrito de VRAM** (perfis calibrados para GPUs de 6 GB como a RTX 4050 Mobile com NVIDIA PRIME Render Offload), **checkpoint/resume atômico por chunks** e uma interface interativa de terminal (**Huh** + **Bubble Tea** + **Lipgloss**).

---

## Principais Funcionalidades

- **Zero Desgaste de SSD (`/dev/shm`):** Extrai e processa janelas de frames em `/dev/shm/aniup_<pid>`, limpando imediatamente os PNGs a cada chunk concluído ou em caso de interrupção (`q`, `Ctrl+C`, `SIGINT`, `SIGTERM`).
- **Checkpoint & Resume Atômico:** Cria um diretório oculto `.aniup_<video>_<scale>x/` com `manifest.json` (SHA256 parcial do vídeo de entrada + status e hash de cada segmento `.mkv`), permitindo retomar exatamente do último chunk concluído.
- **Suporte Nativo a GPU Híbrida (NVIDIA PRIME):** Injeta automaticamente `__NV_PRIME_RENDER_OFFLOAD=1` e `__GLX_VENDOR_LIBRARY_NAME=nvidia` nos processos NCNN Vulkan e codifica os segmentos com `hevc_nvenc` ou `av1_nvenc` (`CQ 19`, `preset p6`, `yuv420p10le`).
- **Diagnóstico Integrado (`aniup doctor`):** Verifica binários, encoders NVENC e espaço livre em `/dev/shm` (mínimo 4 GiB), exibindo comandos diretos de instalação para **Arch Linux / CachyOS** (`pacman` / `paru` / `yay`).
- **Wizard Interativo & Dashboard em Tempo Real:**
  - Formulário interativo (`huh`) quando executado sem todas as flags de CLI.
  - Dashboard (`bubbletea`) com progresso global, ETA, FPS médio, progresso em tempo real do NCNN (`\r`/`\n` scanner) e telemetria de `/dev/shm`, VRAM e temperatura da GPU (`nvidia-smi`).
- **Preservação Completa de Streams:** Ao final de todos os chunks, concatena os segmentos de vídeo sem recodificar e preserva todas as faixas de áudio, legendas e capítulos do arquivo original (`-map 0:v:0 -map 1:a? -map 1:s? -map_chapters 1 -c copy`).

---

## Pré-requisitos (Arch Linux / CachyOS)

```bash
# FFmpeg, FFprobe e utilitários NVIDIA (NVENC + nvidia-smi)
sudo pacman -S ffmpeg nvidia-utils

# Instale pelo menos um motor de upscale NCNN Vulkan (AUR):
paru -S realesrgan-ncnn-vulkan-bin
# e/ou
paru -S realcugan-ncnn-vulkan-bin
```

Para validar o ambiente a qualquer momento:

```bash
aniup doctor
```

---

## Compilação e Instalação

Usando [Task](https://taskfile.dev) (`go-task`):

```bash
# Listar todas as tasks disponíveis
task

# Compilar o binário otimizado em ./bin/aniup
task build

# Instalar em ~/.local/bin/aniup
task install

# Executar testes unitários com race detector e cobertura
task test

# Executar linters (go vet + golangci-lint)
task lint
```

---

## Uso

### 1. Modo Interativo (Wizard TUI)

```bash
aniup
# ou passando apenas o arquivo de entrada:
aniup episodio01.mkv
```

O Wizard guiará pela escolha do arquivo, motor/modelo NCNN instalado, escala (`2x` ou `4x`), perfil de VRAM (`Turbo`, `Balanceado`, `Conservador`), tamanho do chunk em frames (validado contra o espaço livre real em `/dev/shm`) e encoder (`hevc_nvenc` ou `av1_nvenc`).

### 2. Modo Direto via Flags CLI

```bash
aniup \
  -i episodio01.mkv \
  -o episodio01_4k.mkv \
  --engine realesrgan-ncnn-vulkan \
  --model realesr-animevideov3 \
  --scale 2 \
  --profile turbo \
  --chunk-size 600 \
  --encoder hevc_nvenc \
  --resume
```

### Todas as Flags (`aniup --help`)

| Flag | Descrição | Padrão |
| :--- | :--- | :--- |
| `-i`, `--input` | Arquivo de vídeo de entrada (ou argumento posicional) | — |
| `-o`, `--output` | Arquivo de vídeo de saída | `<nome>_<scale>x_<model>.mkv` |
| `--engine` | Motor NCNN (`realesrgan-ncnn-vulkan` \| `realcugan-ncnn-vulkan`) | Wizard |
| `--model` | Modelo (`realesr-animevideov3`, `realesrgan-x4plus-anime`, `models-se`, `models-pro`) | Wizard |
| `--scale` | Escala de upscale (`2` ou `4`) | `2` |
| `--profile` | Perfil de VRAM/Tiling (`turbo` `768/2:3:3`, `balanced` `400/1:2:2`, `conservative` `256/1:1:1`) | `turbo` |
| `--chunk-size` | Quantidade de frames por chunk extraídos em `/dev/shm` | `600` |
| `--encoder` | Encoder NVIDIA NVENC (`hevc_nvenc` \| `av1_nvenc`) | `hevc_nvenc` |
| `--resume` | Retomar automaticamente de um checkpoint existente compatível | `false` |
| `--keep-chunks` | Manter o diretório oculto `.aniup_<video>_<scale>x/` após a concatenação final | `false` |
| `-h`, `--help` | Exibe a ajuda completa de uso | — |

---

## Estrutura do Projeto

```text
aniup/
├── cmd/
│   └── aniup/
│       └── main.go           # Entrypoint CLI, flags, subcomando `doctor` e integração TUI
├── internal/
│   ├── config/
│   │   └── config.go         # Structs de configuração, presets de GPU/VRAM e estimativa de /dev/shm
│   ├── deps/
│   │   └── checker.go        # Diagnóstico de PATH, encoders NVENC, /dev/shm e guia Arch/CachyOS
│   ├── probe/
│   │   └── ffprobe.go        # Extração de resolução, r_frame_rate, total de frames e streams
│   ├── state/
│   │   └── manifest.go       # Checkpoint atômico (.aniup_<nome>_<scale>x/manifest.json) e SHA256 parcial
│   ├── pipeline/
│   │   ├── runner.go         # Orquestrador de chunks em /dev/shm com suporte a context.Context
│   │   ├── extractor.go      # Extração precisa de intervalos de frames via FFmpeg
│   │   ├── upscaler.go       # Wrapper NCNN Vulkan com PRIME offload e parser de progresso (\r/\n)
│   │   └── encoder.go        # Encoding NVENC atômico por chunk + concat demuxer final
│   └── tui/
│       ├── doctor_view.go    # Quadro estilizado (Lipgloss) de diagnóstico e instalação
│       ├── wizard.go         # Formulário interativo inicial (Huh)
│       └── dashboard.go      # Dashboard em tempo real (Bubble Tea) com telemetria de GPU e RAM
├── go.mod
└── Taskfile.yml
```
