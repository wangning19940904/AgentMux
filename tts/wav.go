package tts

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

func wavPCM16ToPCM(data []byte, targetRate int) ([]byte, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	var channels, bits int
	var sampleRate int
	var samples []byte
	for offset := 12; offset+8 <= len(data); {
		name := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		end := start + size
		if size < 0 || end < start || end > len(data) {
			return nil, errors.New("invalid WAV chunk size")
		}
		switch name {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("invalid WAV fmt chunk")
			}
			if binary.LittleEndian.Uint16(data[start:start+2]) != 1 {
				return nil, errors.New("WAV output is not integer PCM")
			}
			channels = int(binary.LittleEndian.Uint16(data[start+2 : start+4]))
			sampleRate = int(binary.LittleEndian.Uint32(data[start+4 : start+8]))
			bits = int(binary.LittleEndian.Uint16(data[start+14 : start+16]))
		case "data":
			samples = data[start:end]
		}
		offset = end
		if offset%2 != 0 {
			offset++
		}
	}
	if (channels != 1 && channels != 2) || bits != 16 || sampleRate <= 0 || len(samples) == 0 {
		return nil, fmt.Errorf("unsupported WAV format: channels=%d rate=%d bits=%d", channels, sampleRate, bits)
	}
	frameBytes := channels * 2
	if len(samples)%frameBytes != 0 {
		return nil, errors.New("WAV data contains an incomplete sample frame")
	}
	inputFrames := len(samples) / frameBytes
	mono := make([]int16, inputFrames)
	for index := range mono {
		left := int32(int16(binary.LittleEndian.Uint16(samples[index*frameBytes : index*frameBytes+2])))
		if channels == 2 {
			right := int32(int16(binary.LittleEndian.Uint16(samples[index*frameBytes+2 : index*frameBytes+4])))
			left = (left + right) / 2
		}
		mono[index] = int16(left)
	}
	if sampleRate != targetRate {
		mono = resampleLinear(mono, sampleRate, targetRate)
	}
	output := make([]byte, len(mono)*2)
	for index, sample := range mono {
		binary.LittleEndian.PutUint16(output[index*2:index*2+2], uint16(sample))
	}
	return output, nil
}

func resampleLinear(input []int16, sourceRate, targetRate int) []int16 {
	if len(input) == 0 || sourceRate <= 0 || targetRate <= 0 || sourceRate == targetRate {
		return append([]int16(nil), input...)
	}
	outputLength := int(math.Round(float64(len(input)) * float64(targetRate) / float64(sourceRate)))
	if outputLength < 1 {
		outputLength = 1
	}
	output := make([]int16, outputLength)
	ratio := float64(sourceRate) / float64(targetRate)
	for index := range output {
		position := float64(index) * ratio
		left := int(position)
		if left >= len(input)-1 {
			output[index] = input[len(input)-1]
			continue
		}
		fraction := position - float64(left)
		value := float64(input[left])*(1-fraction) + float64(input[left+1])*fraction
		output[index] = int16(math.Round(value))
	}
	return output
}
