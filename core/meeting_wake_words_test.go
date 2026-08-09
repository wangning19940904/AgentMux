package core

import (
	"strings"
	"testing"
)

func TestParseMeetingVoiceWakeWordsNormalizesAndDeduplicates(t *testing.T) {
	words, err := ParseMeetingVoiceWakeWords(" 王宁同学，小王小王\nmeeting bot;王宁同学 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"王宁同学", "小王小王", "meeting bot"}
	if strings.Join(words, "|") != strings.Join(want, "|") {
		t.Fatalf("wake words = %#v, want %#v", words, want)
	}
	normalized, err := NormalizeMeetingVoiceWakeWords("王宁同学, 小王小王")
	if err != nil || normalized != "王宁同学\n小王小王" {
		t.Fatalf("normalized wake words = %q, %v", normalized, err)
	}
}

func TestParseMeetingVoiceWakeWordsEnforcesLimits(t *testing.T) {
	if _, err := ParseMeetingVoiceWakeWords(strings.Repeat("醒", maxMeetingVoiceWakeRunes+1)); err == nil {
		t.Fatal("oversized wake word was accepted")
	}
	entries := make([]string, maxMeetingVoiceWakeWords+1)
	for i := range entries {
		entries[i] = "wake-" + string(rune('A'+i))
	}
	if _, err := ParseMeetingVoiceWakeWords(strings.Join(entries, ",")); err == nil {
		t.Fatal("too many wake words were accepted")
	}
}
