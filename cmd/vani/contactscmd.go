package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/contacts"
	"github.com/bharathvbcr/Vani/internal/decide/fmbackend"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/jarvisio"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
)

const maxContactsInput = 64 << 10

// contactsInput is where `vani contacts` reads its request. Tests replace it.
var contactsInput io.Reader = os.Stdin

// catalog is Jarvis for one command. Tests replace openCatalog.
type catalog interface {
	contacts.Store
	Put(ctx context.Context, key, text string) error
}

var openCatalog = func() catalog { return jarvisFromEnv() }

// contactModels builds the decision route and the Claude asker. Tests replace it.
// A nil return with a nil error means that step cannot be asked.
var contactModels = func(ctx context.Context, e *env) (route, claude contacts.Asker, err error) {
	return e.contactModels(ctx)
}

type contactRequest struct {
	Op    string        `json:"op"`
	Key   string        `json:"key,omitempty"`
	Text  string        `json:"text,omitempty"`
	Label string        `json:"label,omitempty"`
	Kind  string        `json:"kind,omitempty"`
	Items []contactItem `json:"items,omitempty"`
}

type contactItem struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type contactChoice struct {
	Status string `json:"status"`
	Key    string `json:"key,omitempty"`
	Text   string `json:"text,omitempty"`
	Review bool   `json:"review,omitempty"`
	Detail string `json:"detail,omitempty"`
	Note   string `json:"note,omitempty"`
}

func jarvisFromEnv() jarvisio.Client {
	return jarvisio.Client{
		Binary:  os.Getenv("VANI_JARVIS_BINARY"),
		Root:    os.Getenv("VANI_JARVIS_ROOT"),
		Bank:    os.Getenv("VANI_JARVIS_BANK"),
		Timeout: 8 * time.Second,
	}
}

func (e *env) cmdContacts(args []string) error {
	fs := e.newFlags("contacts")
	if err := parse(fs, args); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(contactsInput, maxContactsInput+1))
	if err != nil {
		return e.writeReply(codeReply("could not read the request", jarvisio.CodeBadRequest), errAlreadyReported)
	}
	if len(raw) > maxContactsInput {
		return e.writeReply(codeReply("the request is too large", jarvisio.CodeBadRequest), errAlreadyReported)
	}
	var req contactRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		return e.writeReply(codeReply("the request is not a contacts request", jarvisio.CodeBadRequest), errAlreadyReported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch req.Op {
	case "put":
		err := openCatalog().Put(ctx, req.Key, req.Text)
		if err != nil {
			body, code := failReply(err)
			return e.writeReply(body, contactsReported(code))
		}
		return e.writeReply(struct {
			Stored bool `json:"stored"`
		}{true}, nil)
	case "list":
		keys, err := openCatalog().List(ctx)
		if err != nil {
			body, code := failReply(err)
			return e.writeReply(body, contactsReported(code))
		}
		return e.writeReply(struct {
			Keys []string `json:"keys"`
		}{keys}, nil)
	case "get":
		text, err := openCatalog().Get(ctx, req.Key)
		if err != nil {
			body, code := failReply(err)
			return e.writeReply(body, contactsReported(code))
		}
		return e.writeReply(struct {
			Key  string `json:"key"`
			Text string `json:"text"`
		}{req.Key, text}, nil)
	case "import":
		return e.writeReply(e.importContacts(ctx, req.Items), nil)
	case "choose":
		res := e.chooseContact(ctx, contacts.Field{Label: req.Label, Kind: req.Kind})
		return e.writeReply(contactChoice{Status: string(res.Status), Key: res.Key, Text: res.Text, Review: res.Review, Detail: res.Detail, Note: res.Note}, nil)
	default:
		return e.writeReply(codeReply("unknown contacts op", jarvisio.CodeBadRequest), errAlreadyReported)
	}
}

func (e *env) importContacts(ctx context.Context, items []contactItem) any {
	if len(items) > len(contacts.Keys) {
		return codeReply("too many contact values", jarvisio.CodeBadRequest)
	}
	cat := openCatalog()
	stored := []string{}
	type fail struct {
		Key   string `json:"key"`
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	var failed []fail
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			failed = append(failed, fail{Key: item.Key, Code: jarvisio.CodeUnavailable, Error: contacts.NotRunNote})
			break
		}
		err := cat.Put(ctx, item.Key, item.Text)
		if err != nil {
			_, code := failReply(err)
			msg := contacts.NotRunNote
			if jarvisio.BankMissing(err) {
				msg = contacts.NotRunNote + ": bank executable is missing"
			}
			var je *jarvisio.Error
			if errors.As(err, &je) && je.Code == jarvisio.CodeJarvis && !jarvisio.Unavailable(err) && !jarvisio.BankMissing(err) {
				msg = "jarvis refused the input"
			}
			failed = append(failed, fail{Key: item.Key, Code: code, Error: msg})
			if jarvisio.Unavailable(err) {
				break
			}
			continue
		}
		stored = append(stored, item.Key)
	}
	return struct {
		Stored []string `json:"stored"`
		Failed []fail   `json:"failed,omitempty"`
	}{stored, failed}
}

func (e *env) chooseContact(ctx context.Context, f contacts.Field) contacts.Result {
	cat := openCatalog()
	var once sync.Once
	var routeAsk, claudeAsk contacts.Asker
	var modelErr error
	load := func() {
		routeAsk, claudeAsk, modelErr = contactModels(ctx, e)
	}
	route := &lazyAsker{build: func() (contacts.Asker, error) {
		once.Do(load)
		return routeAsk, modelErr
	}}
	claude := &lazyAsker{build: func() (contacts.Asker, error) {
		once.Do(load)
		if modelErr != nil {
			return nil, modelErr
		}
		if claudeAsk == nil {
			return nil, errors.New("claude is not available")
		}
		return claudeAsk, nil
	}}
	return contacts.Choose(ctx, f, cat, route, claude)
}

// lazyAsker builds the model the first time it is asked, so an empty catalog
// or a blocked label never starts one.
type lazyAsker struct {
	build func() (contacts.Asker, error)
	ask   contacts.Asker
	err   error
	done  bool
}

func (l *lazyAsker) Ask(ctx context.Context, q decide.Question) (decide.SlotReading, error) {
	if !l.done {
		l.ask, l.err = l.build()
		l.done = true
	}
	if l.err != nil || l.ask == nil {
		return decide.SlotReading{Reading: decide.Unavailable, Detail: "not_configured"}, nil
	}
	return l.ask.Ask(ctx, q)
}

type slotAsker struct{ d decide.Decider }

func (s slotAsker) Ask(ctx context.Context, q decide.Question) (decide.SlotReading, error) {
	ans, err := s.d.Decide(ctx, q)
	if err != nil {
		return decide.SlotReading{}, err
	}
	rd, ok := ans.Slots[contacts.Slot]
	if !ok {
		return decide.SlotReading{Reading: decide.NotAsked, Detail: "missing_slot"}, nil
	}
	return rd, nil
}

func (e *env) contactModels(ctx context.Context) (contacts.Asker, contacts.Asker, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	desc := map[string]map[string]string{contacts.Slot: contacts.Descriptions}
	var backends []decide.Backend
	backends = append(backends, fmbackend.New(fmbackend.Config{Descriptions: desc}))
	if w.cfg.OpenRouterConfigured() {
		spend, err := decide.NewDailyCap(filepath.Join(w.stateDir, "budget", openRouterBudgetFile), w.cfg.Decide.DailyUSD.OpenRouter)
		if err != nil {
			// A spend file is not required for the on-device model. Liquid is
			// left off rather than failing the whole check closed as a pass.
			w.warnf("contacts: openrouter budget: %v", err)
		} else {
			d, err := egress.New(egress.Config{AllowedHosts: w.cfg.AllowedHosts})
			if err != nil {
				return nil, nil, err
			}
			jb, err := jev.New(jev.Config{
				Name:         w.cfg.Decide.OpenRouterName(),
				Model:        w.cfg.Decide.OpenRouterModel,
				KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
				Getenv:       w.openRouterGetenv,
				Descriptions: desc,
				Spend:        spend,
				SpendKey:     openRouterSpendKey,
			}, d)
			if err != nil {
				w.warnf("contacts: openrouter disabled: %v", err)
			} else {
				backends = append(backends, jb)
			}
		}
	}
	rt := decide.NewRouter(backends...)
	rt.Timeout = 30 * time.Second
	var claude contacts.Asker
	p, err := w.provider(ctx)
	if err != nil {
		w.warnf("contacts: claude is not available: %v", err)
	} else if lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.EffectiveTriageModel()}); err != nil {
		w.warnf("contacts: claude is not available: %v", err)
	} else {
		claude = slotAsker{lb}
	}
	return slotAsker{rt}, claude, nil
}

func codeReply(msg, code string) map[string]string {
	return map[string]string{"error": msg, "code": code}
}

func failReply(err error) (map[string]string, string) {
	var je *jarvisio.Error
	if errors.As(err, &je) {
		msg := je.Message
		if jarvisio.BankMissing(err) {
			msg = "inputs read failed: bank executable is missing"
		}
		return codeReply(msg, je.Code), je.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return codeReply(contacts.NotRunNote, jarvisio.CodeUnavailable), jarvisio.CodeUnavailable
	}
	return codeReply(contacts.NotRunNote, jarvisio.CodeUnavailable), jarvisio.CodeUnavailable
}

// coded makes writeReply exit non-zero without putting err's text on stderr
// a second time. The JSON body already says what failed.
func contactsReported(code string) error {
	_ = code
	return errAlreadyReported
}
