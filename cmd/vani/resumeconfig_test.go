package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// resumeConfig is the one place the rkbresume.Config of every resume command is
// built: the same kb.* keys, validator report, work and lock folders and PDF cap
// for `vani resume` and `vani jobs resume`, and the model only when asked for.
func TestResumeConfigIsBuiltOnceForEveryCommand(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p := &draftLLM{text: resumeReply1}
	resumeSetup(t, "", f, resumePriced{p})
	stateDir := os.Getenv("VANI_STATE_DIR")
	e := &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: stateDir, stdout: io.Discard, stderr: io.Discard}

	asked := 0
	old := resumeProvider
	resumeProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { asked++; return old(ctx, w) }
	t.Cleanup(func() { resumeProvider = old })

	w, err := e.resumeConfig(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	rc := w.RC
	if rc.Model != rkbresume.Model || rc.Provider == nil || rc.NoModel != "" || asked != 1 {
		t.Errorf("with a model: Model %q, provider %v, NoModel %q, asked %d", rc.Model, rc.Provider != nil, rc.NoModel, asked)
	}
	if rc.WorkDir != filepath.Join(stateDir, "resumes", ".tmp") || rc.LockDir != stateDir || rc.MaxPDF != maxResumePDF || rc.AllowBase || rc.Runner != rkbresume.Runner(f) {
		t.Errorf("config = %+v", rc)
	}
	if rc.Bank != w.Cfg.KB.Bank || rc.BaseSelection != w.Cfg.KB.BaseSelection || len(rc.Selections) != 1 || rc.VMReport != "" || w.VMNote == "" {
		t.Errorf("kb keys not carried: %+v note %q", rc, w.VMNote)
	}

	w, err = e.resumeConfig(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if w.RC.Provider != nil || w.RC.Model != "" || asked != 1 {
		t.Errorf("without a model the provider was still built: %v %q asked %d", w.RC.Provider != nil, w.RC.Model, asked)
	}
}

func TestResumeConfigRefusesAnUnsetKnowledgeBaseWithTheConfigCode(t *testing.T) {
	resumeSetup(t, "", &fakeResumeRKB{}, nil)
	cfgPath := os.Getenv("VANI_CONFIG")
	e := &env{configPath: cfgPath, stateDir: os.Getenv("VANI_STATE_DIR"), stdout: io.Discard, stderr: io.Discard}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KB.RKB = ""
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	_, err = e.resumeConfig(context.Background(), false)
	var ce *codedError
	if err == nil || !errors.As(err, &ce) || ce.code != codeConfig {
		t.Fatalf("err = %v, want a coded %q error", err, codeConfig)
	}
}
