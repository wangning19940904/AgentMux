package hostmetrics

import (
	"encoding/binary"
	"testing"
)

func TestParseVMStatMatchesActivityMonitor(t *testing.T) {
	output := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    45900.
Pages active:                                 846120.
Pages inactive:                               842053.
Pages speculative:                              7219.
Pages wired down:                             325502.
Pages purgeable:                               30669.
"Translation faults":                     3087281672.
File-backed pages:                            608966.
Anonymous pages:                             1086426.
Pages occupied by compressor:                1035990.
`
	total := uint64(48) << 30
	got := parseVMStat(output, total)
	if got == nil {
		t.Fatal("expected memory stats")
	}
	wantPages := uint64(1086426-30669) + 325502 + 1035990
	if got.Used != wantPages*16384 {
		t.Fatalf("used = %d, want %d", got.Used, wantPages*16384)
	}
	if got.Percent != 76.84 {
		t.Fatalf("percent = %v", got.Percent)
	}
}

func TestParseVMStatFallsBackWithoutAnonymousPages(t *testing.T) {
	output := "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 100.\nPages inactive: 100.\nPages speculative: 50.\n"
	got := parseVMStat(output, 1000*4096)
	if got == nil || got.Used != 750*4096 {
		t.Fatalf("memory = %+v", got)
	}
	if parseVMStat("nonsense", 1000) != nil {
		t.Fatal("expected nil without counters")
	}
}

func TestParsePSCPU(t *testing.T) {
	got := parsePSCPU("  0.0\n 12.5\n150,0\n bad\n 3.5\n", 4)
	if got == nil || *got != 41.5 {
		t.Fatalf("parsePSCPU = %v, want 41.5", got)
	}
	if got := parsePSCPU("900\n", 2); got == nil || *got != 100 {
		t.Fatalf("clamp = %v", got)
	}
	if parsePSCPU("", 4) != nil {
		t.Fatal("expected nil without samples")
	}
}

func TestParseLoadAvgSysctl(t *testing.T) {
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:], 8806)  // 8.60 * 1024
	binary.LittleEndian.PutUint32(raw[4:], 2048)  // 2.00
	binary.LittleEndian.PutUint32(raw[8:], 1024)  // 1.00
	binary.LittleEndian.PutUint64(raw[16:], 1024) // fscale
	got := parseLoadAvgSysctl(raw)
	if got == nil || got.One != 8.6 || got.Five != 2 || got.Fifteen != 1 {
		t.Fatalf("load = %+v", got)
	}
	if parseLoadAvgSysctl(raw[:12]) != nil {
		t.Fatal("short buffer must be rejected")
	}
}

func TestParseIfList2SumsEthernetInterfaces(t *testing.T) {
	message := func(msgType byte, flags uint32, ifType byte, rx, tx uint64) []byte {
		buf := make([]byte, ifMsghdr2Size)
		binary.LittleEndian.PutUint16(buf[0:], ifMsghdr2Size)
		buf[ifMsghdr2TypeOff] = msgType
		binary.LittleEndian.PutUint32(buf[ifMsghdr2FlagsOff:], flags)
		buf[ifMsghdr2DataOff+ifData64TypeOffset] = ifType
		binary.LittleEndian.PutUint64(buf[ifMsghdr2DataOff+ifData64IBytesOff:], rx)
		binary.LittleEndian.PutUint64(buf[ifMsghdr2DataOff+ifData64OBytesOff:], tx)
		return buf
	}
	address := make([]byte, 20)
	binary.LittleEndian.PutUint16(address[0:], 20)
	address[3] = 0xc // RTM_NEWADDR follows each interface.

	var dump []byte
	dump = append(dump, message(rtmIFInfo2, iffLoopback, 0x18, 1<<30, 1<<30)...) // lo0
	dump = append(dump, address...)
	dump = append(dump, message(rtmIFInfo2, 0, iftEther, 1000, 2000)...) // en0
	dump = append(dump, message(rtmIFInfo2, 0, 0x01, 500, 500)...)       // utun0
	dump = append(dump, message(rtmIFInfo2, 0, iftEther, 30, 40)...)     // en1
	got := parseIfList2(dump)
	if got == nil || got.rx != 1030 || got.tx != 2040 {
		t.Fatalf("counters = %+v", got)
	}
	if parseIfList2(address) != nil {
		t.Fatal("no interfaces must report nothing")
	}
}
