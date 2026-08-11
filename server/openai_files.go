package server

import (
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type openAIFileObject struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Bytes         int64  `json:"bytes"`
	CreatedAt     int64  `json:"created_at"`
	ExpiresAt     any    `json:"expires_at"`
	Filename      string `json:"filename"`
	Purpose       string `json:"purpose"`
	Status        string `json:"status"`
	StatusDetails any    `json:"status_details"`
}

type openAIStoredFile struct {
	object      openAIFileObject
	contentType string
	data        []byte
}

type openAIFileRegistry struct {
	mu    sync.RWMutex
	files map[string]*openAIStoredFile
}

func newOpenAIFileRegistry() *openAIFileRegistry {
	return &openAIFileRegistry{files: map[string]*openAIStoredFile{}}
}

func (r *openAIFileRegistry) put(file openAIStoredFile) {
	r.mu.Lock()
	copy := file
	copy.data = append([]byte(nil), file.data...)
	r.files[file.object.ID] = &copy
	r.mu.Unlock()
}

func (r *openAIFileRegistry) get(id string) (openAIStoredFile, bool) {
	if r == nil {
		return openAIStoredFile{}, false
	}
	r.mu.RLock()
	file := r.files[id]
	if file == nil {
		r.mu.RUnlock()
		return openAIStoredFile{}, false
	}
	copy := *file
	copy.data = append([]byte(nil), file.data...)
	r.mu.RUnlock()
	return copy, true
}

func (r *openAIFileRegistry) list() []openAIFileObject {
	if r == nil {
		return []openAIFileObject{}
	}
	r.mu.RLock()
	files := make([]openAIFileObject, 0, len(r.files))
	for _, file := range r.files {
		files = append(files, file.object)
	}
	r.mu.RUnlock()
	sort.Slice(files, func(i, j int) bool { return files[i].CreatedAt > files[j].CreatedAt })
	return files
}

func (r *openAIFileRegistry) delete(id string) (openAIStoredFile, bool) {
	if r == nil {
		return openAIStoredFile{}, false
	}
	r.mu.Lock()
	file := r.files[id]
	if file != nil {
		delete(r.files, id)
	}
	r.mu.Unlock()
	if file == nil {
		return openAIStoredFile{}, false
	}
	return *file, true
}

func (s *Server) handleOpenAIFileCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOpenAIResponseRequestBytes)
	if err := r.ParseMultipartForm(maxOpenAIResponseRequestBytes); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid multipart file upload: "+err.Error(), "invalid_request_error", "file", "invalid_file")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "file is required", "invalid_request_error", "file", "missing_file")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (25<<20)+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "could not read file", "invalid_request_error", "file", "invalid_file")
		return
	}
	if len(data) > 25<<20 {
		writeOpenAIError(w, http.StatusBadRequest, "file exceeds 25 MiB", "invalid_request_error", "file", "file_too_large")
		return
	}
	purpose := strings.TrimSpace(r.FormValue("purpose"))
	if purpose == "" {
		writeOpenAIError(w, http.StatusBadRequest, "purpose is required", "invalid_request_error", "purpose", "missing_purpose")
		return
	}
	contentType := multipartContentType(header)
	object := openAIFileObject{
		ID: "file-" + randHex(16), Object: "file", Bytes: int64(len(data)), CreatedAt: time.Now().Unix(),
		ExpiresAt: nil, Filename: header.Filename, Purpose: purpose, Status: "processed", StatusDetails: nil,
	}
	s.openAIFiles.put(openAIStoredFile{object: object, contentType: contentType, data: data})
	writeJSON(w, http.StatusOK, object)
}

func multipartContentType(header *multipart.FileHeader) string {
	if header != nil {
		if value := strings.TrimSpace(header.Header.Get("Content-Type")); value != "" {
			return value
		}
	}
	return "application/octet-stream"
}

func (s *Server) handleOpenAIFileList(w http.ResponseWriter, _ *http.Request) {
	data := s.openAIFiles.list()
	var firstID, lastID any
	if len(data) > 0 {
		firstID = data[0].ID
		lastID = data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list", "data": data, "first_id": firstID, "last_id": lastID, "has_more": false,
	})
}

func (s *Server) handleOpenAIFileGet(w http.ResponseWriter, r *http.Request) {
	file, ok := s.openAIFiles.get(r.PathValue("file_id"))
	if !ok {
		writeOpenAIFileNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, file.object)
}

func (s *Server) handleOpenAIFileContent(w http.ResponseWriter, r *http.Request) {
	file, ok := s.openAIFiles.get(r.PathValue("file_id"))
	if !ok {
		writeOpenAIFileNotFound(w)
		return
	}
	w.Header().Set("Content-Type", file.contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(file.object.Filename, `"`, "")+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.data)
}

func (s *Server) handleOpenAIFileDelete(w http.ResponseWriter, r *http.Request) {
	file, ok := s.openAIFiles.delete(r.PathValue("file_id"))
	if !ok {
		writeOpenAIFileNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": file.object.ID, "object": "file", "deleted": true})
}

func writeOpenAIFileNotFound(w http.ResponseWriter) {
	writeOpenAIError(w, http.StatusNotFound, "file not found", "invalid_request_error", "file_id", "file_not_found")
}

func (s *Server) resolveOpenAIFileAttachments(req *openAIResponseRequest) error {
	if req == nil {
		return nil
	}
	for index := range req.parsedInput.Attachments {
		attachment := &req.parsedInput.Attachments[index]
		if !strings.HasPrefix(attachment.URL, "openai-file-id:") {
			continue
		}
		id := strings.TrimPrefix(attachment.URL, "openai-file-id:")
		file, ok := s.openAIFiles.get(id)
		if !ok {
			return &openAIFileReferenceError{id: id}
		}
		attachment.URL = ""
		attachment.Data = file.data
		attachment.Name = file.object.Filename
		attachment.MIMEType = file.contentType
	}
	return nil
}

type openAIFileReferenceError struct{ id string }

func (e *openAIFileReferenceError) Error() string { return "file " + e.id + " was not found" }
