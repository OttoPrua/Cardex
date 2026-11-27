// Package board serves the read-only HTML board. Design §11.
package board

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/store"
)

//go:embed templates/page.html
var templateFS embed.FS

//go:embed static/favicon.svg static/favicon.ico static/apple-touch-icon.png static/mark-light.svg static/mark-dark.svg
var staticFS embed.FS

var pageTmpl = template.Must(template.New("page.html").Funcs(boardFuncs).ParseFS(templateFS, "templates/page.html"))

// logTailBytes is how much of an attempt output.log a GET returns.
const logTailBytes = 32 * 1024

// Handler is the read-only board mounted by serve. It takes no arguments;
// the database is {home}/cardex.db. Home is --home, else CARDEX_HOME, else
// the user directory's .cardex, then the v1-data check.
func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/":
		serveCurrent(w, r, false)
	case path == "/favicon.ico":
		serveStatic(w, r, "static/favicon.ico", "image/x-icon")
	case path == "/favicon.svg":
		serveStatic(w, r, "static/favicon.svg", "image/svg+xml")
	case path == "/apple-touch-icon.png":
		serveStatic(w, r, "static/apple-touch-icon.png", "image/png")
	case path == "/api/work":
		serveCurrent(w, r, true)
	case path == "/channels":
		serveChannels(w, r, false)
	case path == "/api/channels":
		serveChannels(w, r, true)
	case path == "/history":
		serveHistory(w, r, false)
	case path == "/api/history":
		serveHistory(w, r, true)
	case strings.HasPrefix(path, "/cards/"):
		id := strings.TrimPrefix(path, "/cards/")
		if !safeID(id) {
			http.NotFound(w, r)
			return
		}
		serveCard(w, r, id, false)
	case strings.HasPrefix(path, "/api/cards/"):
		id := strings.TrimPrefix(path, "/api/cards/")
		if !safeID(id) {
			http.NotFound(w, r)
			return
		}
		serveCard(w, r, id, true)
	case strings.HasPrefix(path, "/attempts/") && strings.HasSuffix(path, "/output.log"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/attempts/"), "/output.log")
		if !safeID(id) {
			http.NotFound(w, r)
			return
		}
		serveLog(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func serveCurrent(w http.ResponseWriter, r *http.Request, asJSON bool) {
	db, _, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, err := loadCurrent(r.Context(), db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	viewName := boardView(r)
	view.BoardView = viewName
	q := r.URL.Query().Get("view")
	view.ViewChosen = q == "status" || q == "project"
	render(w, pageData{Title: "当前工作", View: viewName, Current: view})
}

// boardView accepts only status and project. Anything else, including an
// empty query, is status. A missing query is still status on the server;
// the page script may then apply localStorage before the first paint.
func boardView(r *http.Request) string {
	if r.URL.Query().Get("view") == "project" {
		return "project"
	}
	return "status"
}

func serveStatic(w http.ResponseWriter, r *http.Request, name, contentType string) {
	body, err := staticFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func markSVG(which string) template.HTML {
	name := "static/mark-light.svg"
	class := "cx-mark cx-mark-light"
	if which == "dark" {
		name = "static/mark-dark.svg"
		class = "cx-mark cx-mark-dark"
	}
	body, err := staticFS.ReadFile(name)
	if err != nil {
		return ""
	}
	s := string(body)
	s = strings.ReplaceAll(s, `xmlns="http://www.w3.org/2000/svg"`, "")
	s = strings.ReplaceAll(s, `width="1024"`, `class="`+class+`" width="28"`)
	s = strings.ReplaceAll(s, `height="1024"`, `height="28"`)
	s = strings.ReplaceAll(s, "http://", "")
	s = strings.ReplaceAll(s, "https://", "")
	return template.HTML(s)
}

func serveChannels(w http.ResponseWriter, r *http.Request, asJSON bool) {
	db, home, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, err := loadChannels(r.Context(), db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	loadSubscriptions(home, view)
	view.Tab = "models"
	if r.URL.Query().Get("tab") == "quota" {
		view.Tab = "quota"
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: "模型与额度", View: "status", Channels: view})
}

func serveCard(w http.ResponseWriter, r *http.Request, id string, asJSON bool) {
	db, _, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, err := loadCard(r.Context(), db, id)
	if errors.Is(err, errNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: view.Title, View: "status", Card: view})
}

func serveHistory(w http.ResponseWriter, r *http.Request, asJSON bool) {
	db, _, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	page := parsePage(q.Get("page"))
	view, err := loadHistory(r.Context(), db, q.Get("q"), q.Get("status"), page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: "历史", View: "status", History: view})
}

func serveLog(w http.ResponseWriter, r *http.Request, id string) {
	db, home, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attempt, err := db.GetAttempt(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dir := roots.AttemptDir(home, id, attempt.PromptPath)
	body, err := readTail(dir, logTailBytes)
	if legacy := filepath.Join(home, "attempts", id); errors.Is(err, os.ErrNotExist) && legacy != dir {
		body, err = readTail(legacy, logTailBytes)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(body)
}

func render(w http.ResponseWriter, page pageData) {
	var body bytes.Buffer
	if err := pageTmpl.ExecuteTemplate(&body, "layout", page); err != nil {
		// Diagnostics are for the local operator, not the browser. A template
		// failure must not publish the partial HTML or start two responses.
		log.Printf("cardex board render failed: %.240q", strings.Join(strings.Fields(err.Error()), " "))
		http.Error(w, "board rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body.Bytes())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func safeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

func parsePage(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 1
	}
	n, err := strconv.ParseInt(raw, 10, strconv.IntSize)
	if err != nil || n < 1 {
		return 1
	}
	return int(n)
}

func readTail(dir string, n int) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open("output.log")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	start := int64(0)
	if info.Size() > int64(n) {
		start = info.Size() - int64(n)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

var (
	dbMu       sync.Mutex
	cachedHome string
	cachedDB   *store.Store
)

// openDB reuses one connection for the resolved home. A different home
// (another test, or a new --home) closes the previous handle first.
func openDB() (*store.Store, string, error) {
	home, err := resolveHome()
	if err != nil {
		return nil, "", err
	}
	dbMu.Lock()
	defer dbMu.Unlock()
	if cachedDB != nil && cachedHome == home {
		return cachedDB, home, nil
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return nil, "", err
	}
	if cachedDB != nil {
		_ = cachedDB.Close()
	}
	cachedDB = db
	cachedHome = home
	return db, home, nil
}

// CloseDB closes the cached connection, if any. serve calls it on the way out;
// on Windows an open database file cannot be removed or replaced.
func CloseDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if cachedDB != nil {
		_ = cachedDB.Close()
	}
	cachedDB = nil
	cachedHome = ""
}

func resolveHome() (string, error) {
	home := homeFromArgs(os.Args[1:])
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if home == "" {
		user, err := proc.UserHome()
		if err != nil {
			return "", err
		}
		home = filepath.Join(user, ".cardex")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	if err := homeguard.Check(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func homeFromArgs(args []string) string {
	home := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--home":
			if i+1 >= len(args) {
				continue
			}
			i++
			home = args[i]
		case strings.HasPrefix(a, "--home="):
			home = strings.TrimPrefix(a, "--home=")
		}
	}
	return home
}

var errNotFound = errors.New("board: not found")
