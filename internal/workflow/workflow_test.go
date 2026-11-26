package workflow

import (
	"context"
	"net/url"
	"testing"
)

func TestSettingsDefaultsAndForm(t *testing.T) {
	s := Parse("")
	if !s.Enabled(Review) || s.Enabled(ReadmeSync) {
		t.Error("defaults: review on, README sync off")
	}
	if s.Param(ReadmeSync, "mode") != "comment" || s.Param(ReadmeSync, "patterns") != "README.md, docs/**/*.md" {
		t.Errorf("param defaults: %q %q", s.Param(ReadmeSync, "mode"), s.Param(ReadmeSync, "patterns"))
	}

	form := url.Values{
		"wf.review.enabled":       {"true"},
		"wf.readme-sync.enabled":  {"true"},
		"wf.readme-sync.mode":     {"commit"},
		"wf.readme-sync.trigger":  {"sometimes"}, // not an option: dropped → default
		"wf.readme-sync.patterns": {"README.md"},
	}
	got := Parse(FromForm(form).JSON())
	if !got.Enabled(ReadmeSync) || got.Param(ReadmeSync, "mode") != "commit" || got.Param(ReadmeSync, "trigger") != "command" ||
		got.Param(ReadmeSync, "patterns") != "README.md" {
		t.Errorf("round trip: %+v", got)
	}
	if Parse(FromForm(url.Values{}).JSON()).Enabled(Review) {
		t.Error("an unticked box disables the workflow")
	}
	if f := got.ForForm(); len(f) != 2 || f[1].Fields[1].Name != "wf.readme-sync.mode" || f[1].Fields[1].Value != "commit" {
		t.Errorf("form fields: %+v", f)
	}
}

func TestParseCommand(t *testing.T) {
	cases := map[string][2]string{
		"@bot #regenerate-readme mention the new flag": {ReadmeSync, "mention the new flag"},
		"@bot /regenerate-readme":                      {ReadmeSync, ""},
		"@bot #review":                                 {Review, ""},
	}
	for body, want := range cases {
		key, instr, ok := ParseCommand(body)
		if !ok || key != want[0] || instr != want[1] {
			t.Errorf("%q: %q %q %v", body, key, instr, ok)
		}
	}
	for _, body := range []string{"@bot what about regenerate-readme?", "see issue #12", "a#review"} {
		if key, _, ok := ParseCommand(body); ok {
			t.Errorf("%q must not be a command, got %s", body, key)
		}
	}
}

func TestOrchestratorCancelsOlderRun(t *testing.T) {
	o := NewOrchestrator()
	ctx1, done1 := o.Start(context.Background(), "1|o/r|5|review")
	ctx2, done2 := o.Start(context.Background(), "1|o/r|5|review")
	if ctx1.Err() == nil {
		t.Error("the older run must be cancelled")
	}
	other, doneOther := o.Start(context.Background(), "1|o/r|5|readme-sync")
	if ctx2.Err() != nil || other.Err() != nil {
		t.Error("the newer run and other workflows keep running")
	}
	done1() // the old run finishing must not unregister the new one
	if len(o.Running()) != 2 {
		t.Errorf("running = %v", o.Running())
	}
	done2()
	doneOther()
	if len(o.Running()) != 0 {
		t.Errorf("running after done = %v", o.Running())
	}
}
