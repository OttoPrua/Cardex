package board

import (
	"bytes"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type recordedHeaders struct {
	*httptest.ResponseRecorder
	codes []int
}

func (w *recordedHeaders) WriteHeader(code int) {
	w.codes = append(w.codes, code)
	w.ResponseRecorder.WriteHeader(code)
}

func (w *recordedHeaders) Write(b []byte) (int, error) {
	if len(w.codes) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseRecorder.Write(b)
}

func TestRenderDoesNotStartAResponseBeforeTemplateSucceeds(t *testing.T) {
	orig := pageTmpl
	out := log.Writer()
	var diagnostics bytes.Buffer
	log.SetOutput(&diagnostics)
	t.Cleanup(func() { pageTmpl = orig; log.SetOutput(out) })
	pageTmpl = template.Must(template.New("broken").Funcs(template.FuncMap{
		"broken": func() (string, error) { return "", errors.New("synthetic rendering error") },
	}).Parse(`{{define "layout"}}partial marker {{broken}} tail{{end}}`))
	w := &recordedHeaders{ResponseRecorder: httptest.NewRecorder()}
	render(w, pageData{})
	if !reflect.DeepEqual(w.codes, []int{http.StatusInternalServerError}) ||
		w.Body.String() != "board rendering failed\n" ||
		!strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("headers=%v content-type=%q body=%q", w.codes, w.Header().Get("Content-Type"), w.Body.String())
	}
	if !strings.Contains(diagnostics.String(), "synthetic rendering error") {
		t.Fatalf("missing short reason: %q", diagnostics.String())
	}
}

func TestRenderSuccessPreservesHTML(t *testing.T) {
	orig := pageTmpl
	t.Cleanup(func() { pageTmpl = orig })
	pageTmpl = template.Must(template.New("good").Parse(`{{define "layout"}}<!doctype html><main>unchanged</main>{{end}}`))
	w := &recordedHeaders{ResponseRecorder: httptest.NewRecorder()}
	render(w, pageData{})
	if !reflect.DeepEqual(w.codes, []int{http.StatusOK}) || w.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		w.Body.String() != "<!doctype html><main>unchanged</main>" {
		t.Fatalf("headers=%v content-type=%q body=%q", w.codes, w.Header().Get("Content-Type"), w.Body.String())
	}
}
