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

	standalone := map[string]string{
		"login": filepath.Join(dir, "login.html"),
		"setup": filepath.Join(dir, "setup.html"),
	}
	withLayout := map[string]string{
		"dashboard":              filepath.Join(dir, "dashboard.html"),
		"ai-integrations/list":   filepath.Join(dir, "ai-integrations", "list.html"),
		"ai-integrations/form":   filepath.Join(dir, "ai-integrations", "form.html"),
	}

	tpls := make(map[string]*template.Template)

	for name, file := range standalone {
		t := template.Must(template.New(filepath.Base(file)).Funcs(funcMap).ParseFiles(file))
		tpls[name] = t
	}

	for name, file := range withLayout {
		t := template.Must(
			template.New("layout.html").Funcs(funcMap).ParseFiles(layoutFile, file),
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
