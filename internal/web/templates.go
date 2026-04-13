package web

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"time"
)

type Templates struct {
	templates map[string]*template.Template
}

var funcMap = template.FuncMap{
	"add": func(a, b int64) int64 { return a + b },
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "Never"
		}
		return t.Format("2006-01-02 15:04")
	},
}

func LoadTemplates(dir string) *Templates {
	layoutFile := filepath.Join(dir, "layout.html")

	pages := map[string]string{
		"login":     filepath.Join(dir, "login.html"),
		"setup":     filepath.Join(dir, "setup.html"),
		"dashboard": filepath.Join(dir, "dashboard.html"),
	}

	tpls := make(map[string]*template.Template)

	// Standalone pages (login, setup — no layout)
	for _, name := range []string{"login", "setup"} {
		t := template.Must(template.New(name + ".html").Funcs(funcMap).ParseFiles(pages[name]))
		tpls[name] = t
	}

	// Pages with layout
	for _, name := range []string{"dashboard"} {
		t := template.Must(
			template.New("layout.html").Funcs(funcMap).ParseFiles(layoutFile, pages[name]),
		)
		tpls[name] = t
	}

	return &Templates{templates: tpls}
}

func (t *Templates) Render(w io.Writer, name string, data any) error {
	tpl, ok := t.templates[name]
	if !ok {
		return fmt.Errorf("template %q not found", name)
	}
	return tpl.Execute(w, data)
}
