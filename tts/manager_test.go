package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadAndSynthesizeIntegration(t *testing.T) {
	if os.Getenv("AGENTMUX_TTS_INTEGRATION") != "1" {
		t.Skip("set AGENTMUX_TTS_INTEGRATION=1 to download and run the local TTS runtime")
	}
	manager := NewManager(t.TempDir(), nil)
	if _, err := manager.Install(context.Background(), "piper-zh-huayan", nil); err != nil {
		t.Fatal(err)
	}
	audio, err := manager.SynthesizePCM(context.Background(), "piper-zh-huayan", "0", "你好，这是本地语音测试。")
	if err != nil {
		t.Fatal(err)
	}
	defer audio.Close()
	pcm, err := io.ReadAll(audio)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) < 24000 || len(pcm)%2 != 0 || bytes.HasPrefix(pcm, []byte("RIFF")) {
		t.Fatalf("invalid 24 kHz PCM output: %d bytes", len(pcm))
	}
}

func TestCatalogAndInstalledState(t *testing.T) {
	manager := NewManager(t.TempDir(), nil)
	catalog := manager.Catalog()
	if len(catalog.Models) != 3 || catalog.Models[0].ID != DefaultLocalModel || catalog.Models[0].Installed {
		t.Fatalf("catalog = %+v", catalog)
	}
	spec, _ := lookupSpec(DefaultLocalModel)
	for _, name := range spec.required {
		path := filepath.Join(manager.modelDir(spec.ID), name)
		if filepath.Ext(name) == "" {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !manager.IsInstalled(DefaultLocalModel) {
		t.Fatal("model should be installed when all required files exist")
	}
}

func TestMeloCatalogUsesDownloadedONNXModel(t *testing.T) {
	spec, ok := lookupSpec("melo-tts-zh-en")
	if !ok {
		t.Fatal("MeloTTS catalog entry is missing")
	}
	args, err := spec.args("/models/melo", "0", "/tmp/out.wav", 2, "你好")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) == 0 || args[0] != "--vits-model=/models/melo/model.onnx" {
		t.Fatalf("MeloTTS model argument = %q", args)
	}
	for _, required := range spec.required {
		if required == "model.int8.onnx" {
			t.Fatal("MeloTTS still treats the Git LFS pointer as an installed model")
		}
	}
}

func TestArchiveTargetRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "/absolute", "safe/../../escape"} {
		if _, err := archiveTarget(root, name); err == nil {
			t.Fatalf("unsafe archive path %q was accepted", name)
		}
	}
	path, err := archiveTarget(root, "model/files/model.onnx")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "model", "files", "model.onnx") {
		t.Fatalf("archive target = %q", path)
	}
}

func TestWavPCM16ToPCMResamplesStereo(t *testing.T) {
	var wave bytes.Buffer
	wave.WriteString("RIFF")
	_ = binary.Write(&wave, binary.LittleEndian, uint32(36+8))
	wave.WriteString("WAVEfmt ")
	_ = binary.Write(&wave, binary.LittleEndian, uint32(16))
	_ = binary.Write(&wave, binary.LittleEndian, uint16(1))
	_ = binary.Write(&wave, binary.LittleEndian, uint16(2))
	_ = binary.Write(&wave, binary.LittleEndian, uint32(12000))
	_ = binary.Write(&wave, binary.LittleEndian, uint32(48000))
	_ = binary.Write(&wave, binary.LittleEndian, uint16(4))
	_ = binary.Write(&wave, binary.LittleEndian, uint16(16))
	wave.WriteString("data")
	_ = binary.Write(&wave, binary.LittleEndian, uint32(8))
	for _, sample := range []int16{1000, 3000, 3000, 5000} {
		_ = binary.Write(&wave, binary.LittleEndian, sample)
	}
	pcm, err := wavPCM16ToPCM(wave.Bytes(), 24000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 8 {
		t.Fatalf("PCM bytes = %d, want 8", len(pcm))
	}
	if got := int16(binary.LittleEndian.Uint16(pcm[:2])); got != 2000 {
		t.Fatalf("first mono sample = %d, want 2000", got)
	}
}
