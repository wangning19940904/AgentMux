package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/superagent"
)

// registerWorkerRelay lets a machine linked to this hub reach the other
// machines linked to it, for the worker API only: an agent on one machine can
// then launch and run workers on another. Both machines must belong to the
// same owner. The hub adds nothing of its own: the target machine authorizes
// the stream as it does every stream the hub opens.
func (s *Server) registerWorkerRelay(mux *http.ServeMux) {
	mux.HandleFunc("GET "+superagent.WorkerRelayDevicesPath, s.handleWorkerRelayDevices)
	mux.HandleFunc(superagent.WorkerRelayPrefix+"{id}/{path...}", s.handleWorkerRelay)
}

// relaySource is the device asking, as the link says, with its record.
func (s *Server) relaySource(w http.ResponseWriter, r *http.Request) (*core.Device, bool) {
	deviceID := linkpkg.DeviceID(r.Context())
	if deviceID == "" || s.st == nil || s.remote == nil {
		writeErr(w, http.StatusUnauthorized, "the worker relay is reached only over a device link")
		return nil, false
	}
	source, err := s.st.GetDevice(r.Context(), deviceID)
	if err != nil || source == nil {
		writeErr(w, http.StatusUnauthorized, "unknown device")
		return nil, false
	}
	return source, true
}

func (s *Server) handleWorkerRelayDevices(w http.ResponseWriter, r *http.Request) {
	source, ok := s.relaySource(w, r)
	if !ok {
		return
	}
	devices, err := s.st.ListDevices(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	listed := make([]superagent.RelayDevice, 0, len(devices))
	for _, device := range devices {
		if host, known := s.remote.Get(device.ID); !known || host.Kind != remotepkg.KindLink {
			continue // Not a machine this hub reaches, such as the hub itself.
		}
		if device.ID != source.ID && device.OwnerAccountID == source.OwnerAccountID {
			listed = append(listed, superagent.RelayDevice{ID: device.ID, Name: device.Name})
		}
	}
	writeJSON(w, http.StatusOK, listed)
}

func (s *Server) handleWorkerRelay(w http.ResponseWriter, r *http.Request) {
	source, ok := s.relaySource(w, r)
	if !ok {
		return
	}
	targetID := strings.TrimSpace(r.PathValue("id"))
	targetPath := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	if !superagent.RelayAllowed(r.Method, targetPath) {
		writeErr(w, http.StatusForbidden, "only the worker API is relayed")
		return
	}
	target, err := s.st.GetDevice(r.Context(), targetID)
	if err != nil || target == nil || target.ID == source.ID || target.OwnerAccountID != source.OwnerAccountID {
		writeErr(w, http.StatusNotFound, "no such machine")
		return
	}
	host, known := s.remote.Get(targetID)
	if !known || host.Kind != remotepkg.KindLink {
		writeErr(w, http.StatusNotFound, "no such machine")
		return
	}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.remote.DialContext(ctx, targetID, network)
		},
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // Invocation streams pass through as they come.
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(&url.URL{Scheme: "http", Host: host.RemoteAddr})
			request.Out.URL.Path, request.Out.URL.RawPath = targetPath, ""
			request.Out.Host = host.RemoteAddr
			request.Out.Header.Del("Authorization")
			request.Out.Header.Set("X-AgentMux-Relayed-From", source.Name)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeErr(w, http.StatusBadGateway, host.Name+": "+err.Error())
		},
	}
	proxy.ServeHTTP(w, r)
}
