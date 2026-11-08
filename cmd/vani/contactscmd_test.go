package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/contacts"
	"github.com/bharathvbcr/Vani/internal/jarvisio"
	"github.com/bharathvbcr/decide"
)

const contactSecret = "contact-secret-example-9f3a"

type fakeCatalog struct {
	keys []string
	text map[string]string
	err  error
	puts int
}

func (f *fakeCatalog) List(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]string(nil), f.keys...), nil
}

func (f *fakeCatalog) Get(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	t, ok := f.text[key]
	if !ok {
		return "", &jarvisio.Error{Code: jarvisio.CodeJarvis, Message: "inputs value is not stored"}
	}
	return t, nil
}

func (f *fakeCatalog) Put(context.Context, string, string) error {
	f.puts++
	return f.err
}

type scriptAsk struct {
	rd  decide.SlotReading
	saw int
}

func (s *scriptAsk) Ask(context.Context, decide.Question) (decide.SlotReading, error) {
	s.saw++
	return s.rd, nil
}

func runContacts(t *testing.T, body string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &env{stdout: &out, stderr: &errb, stateDir: t.TempDir()}
	old := contactsInput
	contactsInput = strings.NewReader(body)
	t.Cleanup(func() { contactsInput = old })
	err := e.cmdContacts(nil)
	code := exitCode("contacts", err)
	return code, out.String(), errb.String()
}

func TestContactsBadRequestDoesNotStore(t *testing.T) {
	code, out, errb := runContacts(t, "{")
	if code == 0 || !strings.Contains(out, jarvisio.CodeBadRequest) || strings.Contains(out, `"stored":true`) {
		t.Fatalf("code %d out %s err %s", code, out, errb)
	}
}

func TestContactsListBankMissingIsNotEmpty(t *testing.T) {
	prev := openCatalog
	openCatalog = func() catalog {
		return &fakeCatalog{err: &jarvisio.Error{Code: jarvisio.CodeJarvis, Message: "inputs read failed: bank executable is missing"}}
	}
	t.Cleanup(func() { openCatalog = prev })
	code, out, errb := runContacts(t, `{"op":"list"}`)
	if code == 0 || strings.Contains(out, `"keys"`) || !strings.Contains(out, "bank executable is missing") {
		t.Fatalf("list looked empty or successful: code %d out %s err %s", code, out, errb)
	}
	if strings.Contains(errb, contactSecret) {
		t.Fatal(errb)
	}
}

func TestContactsPutStillStoresWhenListedReadsFail(t *testing.T) {
	cat := &fakeCatalog{}
	prev := openCatalog
	openCatalog = func() catalog { return cat }
	t.Cleanup(func() { openCatalog = prev })
	code, out, errb := runContacts(t, `{"op":"put","key":"email","text":"`+contactSecret+`"}`)
	if code != 0 || !strings.Contains(out, `"stored":true`) || strings.Contains(out, contactSecret) || strings.Contains(errb, contactSecret) {
		t.Fatalf("code %d out %s err %s puts %d", code, out, errb, cat.puts)
	}
	if cat.puts != 1 {
		t.Fatalf("puts %d", cat.puts)
	}
}

func TestContactsChooseAbstainThenClaude(t *testing.T) {
	cat := &fakeCatalog{keys: []string{"website"}, text: map[string]string{"website": contactSecret}}
	prevCat := openCatalog
	openCatalog = func() catalog { return cat }
	route := &scriptAsk{rd: decide.SlotReading{Reading: decide.Abstained}}
	claude := &scriptAsk{rd: decide.SlotReading{Reading: decide.Answered, Value: "website"}}
	prevModels := contactModels
	contactModels = func(context.Context, *env) (contacts.Asker, contacts.Asker, error) {
		return route, claude, nil
	}
	t.Cleanup(func() {
		openCatalog = prevCat
		contactModels = prevModels
	})
	code, out, errb := runContacts(t, `{"op":"choose","label":"Portfolio URL","kind":"text"}`)
	if code != 0 || claude.saw != 1 || route.saw != 1 {
		t.Fatalf("code %d route %d claude %d out %s", code, route.saw, claude.saw, out)
	}
	var got contactChoice
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != string(contacts.Filled) || got.Key != "website" || !got.Review || got.Text != contactSecret {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(errb, contactSecret) {
		t.Fatal("stderr holds the value")
	}
}

func TestContactsChooseRejectsANameThatIsNotAKey(t *testing.T) {
	cat := &fakeCatalog{keys: []string{"email"}, text: map[string]string{"email": contactSecret}}
	prevCat := openCatalog
	openCatalog = func() catalog { return cat }
	route := &scriptAsk{rd: decide.SlotReading{Reading: decide.Answered, Value: "Ada"}}
	claude := &scriptAsk{rd: decide.SlotReading{Reading: decide.Answered, Value: "email"}}
	prevModels := contactModels
	contactModels = func(context.Context, *env) (contacts.Asker, contacts.Asker, error) {
		return route, claude, nil
	}
	t.Cleanup(func() {
		openCatalog = prevCat
		contactModels = prevModels
	})
	code, out, _ := runContacts(t, `{"op":"choose","label":"Preferred name","kind":"text"}`)
	if code != 0 || claude.saw != 0 || strings.Contains(out, contactSecret) || !strings.Contains(out, `"refused"`) {
		t.Fatalf("code %d claude %d out %s", code, claude.saw, out)
	}
}

func TestContactsServeDownChooseDoesNotPass(t *testing.T) {
	prev := openCatalog
	openCatalog = func() catalog {
		return &fakeCatalog{err: &jarvisio.Error{Code: jarvisio.CodeUnavailable, Message: "jarvis executable is missing"}}
	}
	models := 0
	prevModels := contactModels
	contactModels = func(context.Context, *env) (contacts.Asker, contacts.Asker, error) {
		models++
		return &scriptAsk{rd: decide.SlotReading{Reading: decide.Answered, Value: "email"}}, nil, nil
	}
	t.Cleanup(func() {
		openCatalog = prev
		contactModels = prevModels
	})
	code, out, _ := runContacts(t, `{"op":"choose","label":"Email address","kind":"text"}`)
	if code != 0 || models != 0 || !strings.Contains(out, `"not_run"`) || strings.Contains(out, `"filled"`) {
		t.Fatalf("code %d models %d out %s", code, models, out)
	}
	if !strings.Contains(out, contacts.NotRunNote) {
		t.Fatalf("missing the did-not-run note: %s", out)
	}
}

func TestContactsEmptyCatalogDoesNotStartAModel(t *testing.T) {
	prev := openCatalog
	openCatalog = func() catalog { return &fakeCatalog{} }
	models := 0
	prevModels := contactModels
	contactModels = func(context.Context, *env) (contacts.Asker, contacts.Asker, error) {
		models++
		t.Fatal("model started for an empty catalog")
		return nil, nil, nil
	}
	t.Cleanup(func() {
		openCatalog = prev
		contactModels = prevModels
	})
	code, out, _ := runContacts(t, `{"op":"choose","label":"Hobby","kind":"text"}`)
	if code != 0 || models != 0 || !strings.Contains(out, `"empty"`) || strings.Contains(out, `"filled"`) {
		t.Fatalf("code %d models %d out %s", code, models, out)
	}
}
