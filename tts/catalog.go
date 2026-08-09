// Package tts manages downloadable, on-device text-to-speech models.
//
// The built-in catalog deliberately contains only models that can run through
// the same sherpa-onnx executable. This keeps local mode self-contained: a
// model shown as downloadable is also runnable after the download completes.
package tts

import (
	"fmt"
	"runtime"
)

const (
	DefaultLocalModel = "kokoro-82m-zh-int8"
	DefaultLocalVoice = "3"
	runtimeVersion    = "1.13.2"
	releaseBaseURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download"
)

// Voice is a selectable speaker exposed by a local model.
type Voice struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Notes string `json:"notes,omitempty"`
}

// Model describes one downloadable local TTS model.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Languages     []string `json:"languages"`
	Parameters    string   `json:"parameters,omitempty"`
	DownloadBytes int64    `json:"download_bytes"`
	License       string   `json:"license"`
	Engine        string   `json:"engine"`
	Recommended   bool     `json:"recommended,omitempty"`
	Voices        []Voice  `json:"voices"`
}

type modelSpec struct {
	Model
	archiveURL  string
	archiveRoot string
	sha256      string
	required    []string
	args        func(modelDir, voice, output string, threads int, text string) ([]string, error)
}

var modelCatalog = []modelSpec{
	{
		Model: Model{
			ID: "kokoro-82m-zh-int8", Name: "Kokoro 82M 中文（int8）",
			Description: "中英双语、103 个说话人；轻量模型中自然度最好。",
			Languages:   []string{"zh-CN", "en"}, Parameters: "82M / int8",
			DownloadBytes: 147031220, License: "Apache-2.0", Engine: "Kokoro", Recommended: true,
			Voices: []Voice{
				{ID: "3", Name: "中文女声 01", Notes: "zf_001"},
				{ID: "12", Name: "中文女声 18", Notes: "zf_018"},
				{ID: "28", Name: "中文女声 44", Notes: "zf_044"},
				{ID: "37", Name: "中文女声 70", Notes: "zf_070"},
				{ID: "58", Name: "中文男声 09", Notes: "zm_009"},
				{ID: "67", Name: "中文男声 25", Notes: "zm_025"},
				{ID: "79", Name: "中文男声 53", Notes: "zm_053"},
				{ID: "95", Name: "中文男声 82", Notes: "zm_082"},
			},
		},
		archiveURL:  releaseBaseURL + "/tts-models/kokoro-int8-multi-lang-v1_1.tar.bz2",
		archiveRoot: "kokoro-int8-multi-lang-v1_1",
		sha256:      "a1e94694776049035c4f2c6529f003aaece993c76aae9a78995831c3c4dcafc6",
		required:    []string{"model.int8.onnx", "voices.bin", "tokens.txt", "lexicon-zh.txt", "espeak-ng-data"},
		args: func(dir, voice, output string, threads int, text string) ([]string, error) {
			sid, err := validateVoice(voice, 103, DefaultLocalVoice)
			if err != nil {
				return nil, err
			}
			return []string{
				"--kokoro-model=" + join(dir, "model.int8.onnx"),
				"--kokoro-voices=" + join(dir, "voices.bin"),
				"--kokoro-tokens=" + join(dir, "tokens.txt"),
				"--kokoro-data-dir=" + join(dir, "espeak-ng-data"),
				"--kokoro-lexicon=" + join(dir, "lexicon-us-en.txt") + "," + join(dir, "lexicon-zh.txt"),
				"--tts-rule-fsts=" + join(dir, "date-zh.fst") + "," + join(dir, "number-zh.fst") + "," + join(dir, "phone-zh.fst"),
				fmt.Sprintf("--num-threads=%d", threads), "--sid=" + sid,
				"--output-filename=" + output, text,
			}, nil
		},
	},
	{
		Model: Model{
			ID: "melo-tts-zh-en", Name: "MeloTTS 中英",
			Description: "单女声中英混读，发音稳定，适合通知和会议播报。",
			Languages:   []string{"zh-CN", "en"}, Parameters: "VITS",
			DownloadBytes: 167006755, License: "MIT", Engine: "VITS",
			Voices: []Voice{{ID: "0", Name: "默认中文女声"}},
		},
		archiveURL:  releaseBaseURL + "/tts-models/vits-melo-tts-zh_en.tar.bz2",
		archiveRoot: "vits-melo-tts-zh_en",
		required:    []string{"model.onnx", "tokens.txt", "lexicon.txt"},
		args: func(dir, voice, output string, threads int, text string) ([]string, error) {
			sid, err := validateVoice(voice, 1, "0")
			if err != nil {
				return nil, err
			}
			return []string{
				"--vits-model=" + join(dir, "model.onnx"),
				"--vits-lexicon=" + join(dir, "lexicon.txt"),
				"--vits-tokens=" + join(dir, "tokens.txt"),
				"--tts-rule-fsts=" + join(dir, "date.fst") + "," + join(dir, "number.fst") + "," + join(dir, "phone.fst"),
				fmt.Sprintf("--num-threads=%d", threads), "--sid=" + sid,
				"--output-filename=" + output, text,
			}, nil
		},
	},
	{
		Model: Model{
			ID: "piper-zh-huayan", Name: "Piper 华研中文",
			Description: "启动快、资源占用最低，适合低配设备和高频短播报。",
			Languages:   []string{"zh-CN"}, Parameters: "VITS / medium",
			DownloadBytes: 67255926, License: "See model card", Engine: "Piper",
			Voices: []Voice{{ID: "0", Name: "华研"}},
		},
		archiveURL:  releaseBaseURL + "/tts-models/vits-piper-zh_CN-huayan-medium.tar.bz2",
		archiveRoot: "vits-piper-zh_CN-huayan-medium",
		required:    []string{"zh_CN-huayan-medium.onnx", "tokens.txt", "espeak-ng-data", "MODEL_CARD"},
		args: func(dir, voice, output string, threads int, text string) ([]string, error) {
			sid, err := validateVoice(voice, 1, "0")
			if err != nil {
				return nil, err
			}
			return []string{
				"--vits-model=" + join(dir, "zh_CN-huayan-medium.onnx"),
				"--vits-tokens=" + join(dir, "tokens.txt"),
				"--vits-data-dir=" + join(dir, "espeak-ng-data"),
				fmt.Sprintf("--num-threads=%d", threads), "--sid=" + sid,
				"--output-filename=" + output, text,
			}, nil
		},
	},
}

type runtimeAsset struct {
	url        string
	bytes      int64
	sha256     string
	archive    bool
	binaryName string
}

func currentRuntimeAsset() (runtimeAsset, bool) {
	name := ""
	size := int64(0)
	digest := ""
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		name, size, digest = "sherpa-onnx-v1.13.2-osx-arm64-shared.tar.bz2", 25914829, "50c5c04d93113602432a13454d6bf8e5d2624206b985fbd0dd4698454ae6c509"
	case "darwin/amd64":
		name, size, digest = "sherpa-onnx-v1.13.2-osx-x64-shared.tar.bz2", 28956076, "5accc61eca4a69fc8860f2078c55f61ca96f67b4c311badffa0d6924ca6e1911"
	case "linux/amd64":
		name, size, digest = "sherpa-onnx-v1.13.2-linux-x64-shared.tar.bz2", 26825365, "1ef6741535f7af4d69e394fd440a807108036d26ed4f542660191019da5c0daa"
	case "linux/arm64":
		name, size, digest = "sherpa-onnx-v1.13.2-linux-aarch64-shared-cpu.tar.bz2", 26674802, "b54178420e9e6ff6c7f308b5f1cde827215b38393356ee0bd2b7595c648b330b"
	case "windows/amd64":
		name, size, digest = "sherpa-onnx-non-streaming-tts-x64-v1.13.2.exe", 21416960, "6810e679770fda158d66d6d526d76a8b047fbb1979d29325459b4da6d3094555"
		return runtimeAsset{url: releaseBaseURL + "/v" + runtimeVersion + "/" + name, bytes: size, sha256: digest, binaryName: name}, true
	default:
		return runtimeAsset{}, false
	}
	return runtimeAsset{url: releaseBaseURL + "/v" + runtimeVersion + "/" + name, bytes: size, sha256: digest, archive: true, binaryName: "sherpa-onnx-offline-tts"}, true
}

func lookupSpec(id string) (modelSpec, bool) {
	for _, spec := range modelCatalog {
		if spec.ID == id {
			return spec, true
		}
	}
	return modelSpec{}, false
}

// Lookup reports whether id is a model managed by the built-in local runtime.
func Lookup(id string) (Model, bool) {
	spec, ok := lookupSpec(id)
	return spec.Model, ok
}
