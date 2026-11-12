// Package link connects a node to a hub over one WebSocket the node opens
// outward, so a node needs no public address. The hub then opens streams back
// down that connection, each carrying one HTTP exchange with the node's API.
package link

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	// ProtocolVersion changes when hub and node can no longer talk.
	ProtocolVersion = "1"
	// PairPath exchanges a pairing code for a device token.
	PairPath = "/link/v1/pair"
	// ConnectPath upgrades to the multiplexed connection.
	ConnectPath = "/link/v1/connect"

	headerProtocol = "X-AgentMux-Link-Protocol"
	headerInfo     = "X-AgentMux-Device-Info"

	writeTimeout  = 15 * time.Second
	maxInfoHeader = 4 << 10
)

// ErrOffline means the device is paired but has no connection to the hub now.
var ErrOffline = errors.New("device is offline")

// ErrRevoked means the hub no longer accepts this device's token. A node stops
// reconnecting when it sees it.
var ErrRevoked = errors.New("the hub no longer accepts this device")

// wsStream presents a WebSocket as a byte stream for the multiplexer. The
// multiplexer reads from one goroutine and writes from another, which is what
// a WebSocket connection allows.
type wsStream struct {
	ws      *websocket.Conn
	reader  io.Reader
	writeMu sync.Mutex
}

func (s *wsStream) Read(p []byte) (int, error) {
	for {
		if s.reader == nil {
			kind, reader, err := s.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if kind != websocket.BinaryMessage {
				continue
			}
			s.reader = reader
		}
		n, err := s.reader.Read(p)
		if err == io.EOF {
			s.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *wsStream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return 0, err
	}
	if err := s.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *wsStream) Close() error { return s.ws.Close() }

// muxConfig keeps a dead connection from looking alive: either side closes
// the session when a keepalive goes unanswered, and the node then reconnects.
func muxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	config.EnableKeepAlive = true
	config.KeepAliveInterval = 15 * time.Second
	config.ConnectionWriteTimeout = writeTimeout
	config.LogOutput = io.Discard
	return config
}

// LocalDeviceInfo describes this machine to a hub.
func LocalDeviceInfo(name, version string) core.DeviceInfo {
	hostname, _ := os.Hostname()
	name = strings.TrimSpace(name)
	if name == "" {
		name = hostname
	}
	return core.DeviceInfo{Name: name, Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: version}
}

func encodeInfo(info core.DeviceInfo) string {
	data, _ := json.Marshal(info)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeInfo(value string) core.DeviceInfo {
	var info core.DeviceInfo
	if value == "" || len(value) > maxInfoHeader {
		return info
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return info
	}
	_ = json.Unmarshal(data, &info)
	return cleanInfo(info)
}

// cleanInfo bounds what a node can put in the hub's device list.
func cleanInfo(info core.DeviceInfo) core.DeviceInfo {
	trim := func(value string, limit int) string {
		value = strings.TrimSpace(value)
		if len(value) > limit {
			value = value[:limit]
		}
		return value
	}
	return core.DeviceInfo{
		Name: trim(info.Name, 120), Hostname: trim(info.Hostname, 255),
		OS: trim(info.OS, 32), Arch: trim(info.Arch, 32), Version: trim(info.Version, 64),
	}
}
