// Package board serves the read-only HTML board. Design §11.
package board

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"cardex/internal/store"
)

//go:embed templates/page.html
var templateFS embed.FS

var pageTmpl = template.Must(template.ParseFS(templateFS, "templates/page.html"))

// logTailBytes is how much of an attempt output.log a GET returns.
const logTailBytes = 32 * 1024

// Handler is the read-only board mounted by serve. It takes no arguments;
// the database is {home}/cardex.db, with home from --home or CARDEX_HOME.
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
	render(w, pageData{Title: "当前工作", Current: view})
}

func serveChannels(w http.ResponseWriter, r *http.Request, asJSON bool) {
	db, _, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, err := loadChannels(r.Context(), db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: "渠道", Channels: view})
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
	render(w, pageData{Title: view.Title, Card: view})
}

func serveHistory(w http.ResponseWriter, r *http.Request, asJSON bool) {
	db, _, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	view, err := loadHistory(r.Context(), db, q.Get("q"), q.Get("status"), page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: "历史", History: view})
}

func serveLog(w http.ResponseWriter, r *http.Request, id string) {
	db, home, err := openDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := db.GetAttempt(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := filepath.Join(home, "attempts", id, "output.log")
	body, err := readTail(path, logTailBytes)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.ExecuteTemplate(w, "layout", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func safeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

func readTail(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
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

func resolveHome() (string, error) {
	home := homeFromArgs(os.Args[1:])
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if home == "" {
		return "", errors.New("missing --home or CARDEX_HOME")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	user, err := os.UserHomeDir()
	if err == nil {
		banned := filepath.Join(user, ".cardex")
		if abs == banned || strings.HasPrefix(abs, banned+string(filepath.Separator)) {
			return "", errors.New("refusing to use ~/.cardex")
		}
	}
	return abs, nil
}

func homeFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--home" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(a, "--home=") {
			return strings.TrimPrefix(a, "--home=")
		}
	}
	return ""
}

var errNotFound = errors.New("board: not found")
