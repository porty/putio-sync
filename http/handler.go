package http

import (
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/porty/putio-sync/sync"
)

type Handler struct {
	// synchronization client which is used to start/stop downloading
	sync *sync.Client

	// HTTP request multiplexer
	mux *http.ServeMux

	// Bundled copy of the web UI, which are served as static files
	staticFS http.FileSystem
}

func NewHandler(s *sync.Client) *Handler {
	h := &Handler{
		sync:     s,
		mux:      http.NewServeMux(),
		staticFS: FS(false),
	}
	h.mux.HandleFunc("/api/start", h.handleStart)
	h.mux.HandleFunc("/api/stop", h.handleStop)
	h.mux.HandleFunc("/api/list-downloads", h.handleListDownloads)
	h.mux.HandleFunc("/api/config", h.handleConfig)
	h.mux.HandleFunc("/api/logout", h.handleLogout)
	h.mux.HandleFunc("/api/clear", h.handleClear)
	h.mux.HandleFunc("/api/tree", h.handleTree)
	h.mux.HandleFunc("/api/ping", h.handlePing)
	h.mux.HandleFunc("/api/go-to-file", h.handleGoToFile)
	h.mux.HandleFunc("/api/add-magnet", h.handleAddMagnet)
	h.mux.HandleFunc("/api/add-torrent", h.handleAddTorrent)

	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	apiHandler := CORSMiddleware(JSONMiddleware(h.mux))
	fsHandler := CORSMiddleware(http.FileServer(h.staticFS))

	if strings.HasPrefix(r.URL.Path, "/api/") {
		apiHandler.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/welcome") {
		r.URL.Path = "/"
		fsHandler.ServeHTTP(w, r)
		return
	}

	// dont serve static files on debug mode. Nodejs development server handles that
	if !h.sync.Debug {
		fsHandler.ServeHTTP(w, r)
		return
	}

	http.NotFound(w, r)
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("start called\n")

	err := h.sync.Run()
	if err != nil {
		h.sync.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var response = struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}
	err = json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	return
}

func (h *Handler) handleStop(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("stop called\n")

	err := h.sync.Stop()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var response = struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}
	err = json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	return
}

type ByDate []*sync.State

func (d ByDate) Len() int      { return len(d) }
func (d ByDate) Swap(i, j int) { d[i], d[j] = d[j], d[i] }
func (d ByDate) Less(i, j int) bool {
	di, dj := d[i], d[j]
	if di.DownloadStatus == dj.DownloadStatus {
		return di.DownloadFinishedAt.After(dj.DownloadFinishedAt)
	}
	return di.DownloadStatus < dj.DownloadStatus
}

func (h *Handler) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	states, err := h.sync.Store.States(h.sync.User.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Sort(ByDate(states))

	now := time.Now().UTC()
	var totalSpeed float64
	for _, state := range states {
		if state.DownloadStatus != sync.DownloadInProgress {
			continue
		}

		bytes := float64(state.BytesTransferredSinceLastUpdate / 1024)
		duration := now.Sub(state.DownloadStartedAt).Seconds()
		state.DownloadSpeed = bytes / duration

		totalSpeed += state.DownloadSpeed
	}

	listResponse := struct {
		Status     string        `json:"status"`
		TotalSpeed float64       `json:"total_speed"`
		Files      []*sync.State `json:"files"`
	}{
		Status:     h.sync.Status(),
		TotalSpeed: totalSpeed,
		Files:      states,
	}
	err = json.NewEncoder(w).Encode(&listResponse)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	return
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		err := json.NewEncoder(w).Encode(h.sync.Config)
		if err != nil {
			h.sync.Printf("Error encoding config: %v\n", err)
			http.Error(w, "", http.StatusInternalServerError)
		}
		return
	}

	if r.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}

	// New configuration POST'ed

	var c sync.Config
	err := json.NewDecoder(r.Body).Decode(&c)
	if err != nil {
		h.sync.Printf("Error decoding config: %v\n", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	if c.OAuth2Token != "" {
		h.sync.Config.OAuth2Token = c.OAuth2Token
		// RenewToken is called here since a new OAuth2 token is inplace and a
		// new client associated with this token must be created.
		err = h.sync.RenewToken()
		if err != nil {
			h.sync.Printf("Error renewing token: %v\n", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if c.PollInterval >= sync.Duration(time.Minute) {
		h.sync.Config.PollInterval = c.PollInterval
	}

	if c.DownloadTo != "" {
		h.sync.Config.DownloadTo = c.DownloadTo
	}

	if c.DownloadFrom >= 0 {
		h.sync.Config.DownloadFrom = c.DownloadFrom
	}

	if c.SegmentsPerFile > 0 {
		h.sync.Config.SegmentsPerFile = c.SegmentsPerFile
	}

	if c.MaxParallelFiles > 0 {
		oldmax, newmax := int(h.sync.Config.MaxParallelFiles), int(c.MaxParallelFiles)
		h.sync.Config.MaxParallelFiles = c.MaxParallelFiles
		err = h.sync.AdjustConcurreny(newmax - oldmax)
		if err != nil {
			h.sync.Printf("Error setting max parallel files: %v\n", err)
			http.Error(w, "Error setting max parallel files", http.StatusBadRequest)
			return
		}
	}

	h.sync.Config.IsPaused = c.IsPaused

	h.sync.Config.DeleteRemoteFile = c.DeleteRemoteFile

	err = h.sync.Store.SaveConfig(h.sync.Config, h.sync.User.Username)
	if err != nil {
		h.sync.Printf("Error saving config: %v\n", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	response := struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}
	err = json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, "", http.StatusInternalServerError)
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "", http.StatusMethodNotAllowed)

	}

	_ = h.sync.Stop()
	_ = h.sync.DeleteToken()

	response := struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}
	err := json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, "", http.StatusInternalServerError)
	}
	return
}

func (h *Handler) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Unsupported method", http.StatusBadRequest)
		return
	}

	states, err := h.sync.Store.States(h.sync.User.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, state := range states {
		if state.DownloadStatus != sync.DownloadCompleted {
			continue
		}
		state.IsHidden = true
		_ = h.sync.Store.SaveState(state, h.sync.User.Username)
	}

	response := struct {
		Status string `json:"status"`
	}{
		Status: "ok",
	}
	err = json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
	}
	return
}

func (h *Handler) handleAddMagnet(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("add-magnet called\n")

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uri := r.FormValue("url")
	if uri == "" {
		http.Error(w, "empty magnet uri", http.StatusBadRequest)
		return
	}

	magnetURI, err := base64.URLEncoding.DecodeString(uri)
	if err != nil {
		h.sync.Printf("Error decoding url: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	transfer, err := h.sync.C.Transfers.Add(nil, string(magnetURI), h.sync.Config.DownloadFrom, "")
	if err != nil {
		h.sync.Printf("Error adding a new transfer: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = json.NewEncoder(w).Encode(&transfer)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	return
}

func (h *Handler) handleAddTorrent(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("add-magnet called\n")

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	torrentPath := r.FormValue("path")
	if torrentPath == "" {
		http.Error(w, "empty torrent path", http.StatusBadRequest)
		return
	}

	b, err := base64.URLEncoding.DecodeString(torrentPath)
	if err != nil {
		h.sync.Printf("Error decoding path: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	torrentPath = string(b)

	if !exists(torrentPath) {
		http.Error(w, "file not found", http.StatusBadRequest)
		return
	}

	f, err := os.Open(torrentPath)
	if err != nil {
		h.sync.Printf("Error opening file: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	_, filename := filepath.Split(torrentPath)
	upload, err := h.sync.C.Files.Upload(nil, f, filename, h.sync.Config.DownloadFrom)
	if err != nil {
		h.sync.Printf("Error uploading file: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = json.NewEncoder(w).Encode(upload.Transfer)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	return
}

func (h *Handler) handlePing(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("ping called\n")

	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, err := h.sync.C.Account.Info(nil)
	if err != nil {
		h.sync.Printf("Error fetching account info: %v\n", err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	return
}

func (h *Handler) handleGoToFile(w http.ResponseWriter, r *http.Request) {
	h.sync.Debugf("go-to-file called\n")

	if r.Method != "GET" {
		http.Error(w, "method now allowed", http.StatusMethodNotAllowed)
		return
	}

	fileID, err := strconv.ParseInt(r.FormValue("id"), 0, 64)
	if err != nil {
		h.sync.Debugf("invalid file id: %v\n", err)
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}

	state, err := h.sync.Store.State(fileID, h.sync.User.Username)
	if err == sync.ErrStateNotFound {
		h.sync.Debugf("fetching state failed for %v: %v\n", fileID, err)
		http.Error(w, "file not found", http.StatusBadRequest)
		return
	}

	if err != nil {
		h.sync.Debugf("fetching state failed for %v: %v\n", fileID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	}

	if cmd == "" {
		h.sync.Debugf("can't open file for this OS\n")
		http.Error(w, "cant open file for this OS", http.StatusInternalServerError)
		return
	}

	_ = exec.Command(cmd, state.LocalPath).Run()
}

func (h *Handler) handleTree(w http.ResponseWriter, r *http.Request) {
	parent := r.FormValue("parent")
	if parent == "" {
		parent = "/"
	}

	files, err := ioutil.ReadDir(parent)
	if err != nil {
		h.sync.Println(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type folder struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Parent string `json:"parent"`
	}

	var folders []folder
	for _, f := range files {
		if !f.Mode().IsDir() {
			continue
		}

		folders = append(folders, folder{
			Name:   f.Name(),
			Path:   filepath.Join(parent, f.Name()),
			Parent: parent,
		})
	}

	var response = struct {
		Parent  string   `json:"parent"`
		Folders []folder `json:"folders"`
	}{
		Parent:  parent,
		Folders: folders,
	}

	err = json.NewEncoder(w).Encode(&response)
	if err != nil {
		h.sync.Printf("Error encoding response: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	return
}

func exists(filename string) bool {
	_, err := os.Stat(filename)
	return !os.IsNotExist(err)
}
