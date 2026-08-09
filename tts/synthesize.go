package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"
)

const synthesisFileLimit = 64 << 20

// SynthesizePCM generates mono 24 kHz signed 16-bit little-endian PCM, the
// format consumed by the meeting realtime API.
func (m *Manager) SynthesizePCM(ctx context.Context, modelID, voice, text string) (io.ReadCloser, error) {
	spec, ok := lookupSpec(strings.TrimSpace(modelID))
	if !ok {
		return nil, fmt.Errorf("unknown local TTS model %q", modelID)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if utf8.RuneCountInString(text) > 10000 {
		return nil, errors.New("local TTS input exceeds 10000 characters")
	}
	asset, supported := currentRuntimeAsset()
	if !supported {
		return nil, fmt.Errorf("local TTS is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if !m.runtimeInstalled(asset) {
		return nil, errors.New("local TTS runtime is not installed; download a model in the App first")
	}
	if !m.modelInstalled(spec) {
		return nil, fmt.Errorf("local TTS model %q is not downloaded", modelID)
	}
	tmpDir := filepath.Join(m.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	output, err := os.CreateTemp(tmpDir, "speech-*.wav")
	if err != nil {
		return nil, err
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	defer os.Remove(outputPath)
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	args, err := spec.args(m.modelDir(spec.ID), voice, outputPath, threads, text)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, m.runtimeBinary(asset), args...)
	command.Env = runtimeEnvironment(os.Environ(), m.runtimeDir())
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 4096 {
			detail = detail[len(detail)-4096:]
		}
		if detail != "" {
			return nil, fmt.Errorf("run local TTS: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("run local TTS: %w", err)
	}
	wave, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read local TTS output: %w", err)
	}
	if len(wave) > synthesisFileLimit {
		return nil, errors.New("local TTS output exceeds 64 MiB")
	}
	pcm, err := wavPCM16ToPCM(wave, 24000)
	if err != nil {
		return nil, fmt.Errorf("decode local TTS output: %w", err)
	}
	return io.NopCloser(bytes.NewReader(pcm)), nil
}

func runtimeEnvironment(environment []string, runtimeDir string) []string {
	key := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		key = "DYLD_LIBRARY_PATH"
	}
	lib := filepath.Join(runtimeDir, "lib")
	for index, value := range environment {
		if strings.HasPrefix(value, key+"=") {
			environment[index] = key + "=" + lib + string(os.PathListSeparator) + strings.TrimPrefix(value, key+"=")
			return environment
		}
	}
	return append(environment, key+"="+lib)
}

func validateVoice(value string, count int, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	id, err := strconv.Atoi(value)
	if err != nil || id < 0 || id >= count {
		return "", fmt.Errorf("invalid local TTS speaker %q", value)
	}
	return strconv.Itoa(id), nil
}

func join(base, name string) string { return filepath.Join(base, name) }
