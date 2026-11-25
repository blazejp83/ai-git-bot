package web

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Templates struct {
	templates map[string]*template.Template
}

var funcMap = template.FuncMap{
	"add": func(a, b int64) int64 { return a + b },
	"inc": func(i int) int { return i + 1 },
	"dec": func(i int) int { return i - 1 },
	// num formats a count with thousands separators: 1234567 -> 1,234,567.
	"num": func(n int64) string {
		s := strconv.FormatInt(n, 10)
		neg := strings.HasPrefix(s, "-")
		s = strings.TrimPrefix(s, "-")
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		if neg {
			s = "-" + s
		}
		return s
	},
	// usd formats an estimated cost; small amounts keep enough digits to be visible.
	"usd": func(v float64) string {
		switch {
		case v == 0:
			return "$0"
		case v < 0.01:
			return fmt.Sprintf("$%.4f", v)
		default:
			return fmt.Sprintf("$%.2f", v)
		}
	},
	"pct": func(v float64) string { return fmt.Sprintf("%.0f%%", v*100) },
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
		"dashboard":             filepath.Join(dir, "dashboard.html"),
		"ai-integrations/list":  filepath.Join(dir, "ai-integrations", "list.html"),
		"ai-integrations/form":  filepath.Join(dir, "ai-integrations", "form.html"),
		"bots/list":             filepath.Join(dir, "bots", "list.html"),
		"bots/form":             filepath.Join(dir, "bots", "form.html"),
		"git-integrations/list": filepath.Join(dir, "git-integrations", "list.html"),
		"git-integrations/form": filepath.Join(dir, "git-integrations", "form.html"),
		"usage/list":            filepath.Join(dir, "usage", "list.html"),
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
