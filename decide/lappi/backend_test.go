package lappi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/decide"
)

const triageTask = "career.jd_triage"

func triageQuestion() decide.Question {
	return decide.Question{
		Task:     triageTask,
		Prompt:   "Classify the posting.",
		Context:  []byte("Title: Engineer\nLocation: Remote\n\nBuild things."),
		Slots:    []decide.Slot{{Name: "work_mode", Options: []string{"remote", "hybrid", "onsite", "not_stated"}}, {Name: "seniority", Options: []string{"mid", "senior"}}},
		RecordID: "gh:acme:123",
	}
}

func okReply(workMode, seniority string, degraded bool) string {
	slot := func(v string) string {
		if v == "" {
			return `{"value":null,"conformal_set":null,"score":0.1,"noul":true,"degraded":false}`
		}
		return `{"value":"` + v + `","conformal_set":["` + v + `"],"score":0.8,"noul":false,"degraded":false}`
	}
	d := "false"
	if degraded {
		d = "true"
	}
	return `{"status":"ok","schema_version":1,"backend":"qd-metal/test","degraded":` + d +
		`,"slots":{"work_mode":` + slot(workMode) + `,"seniority":` + slot(seniority) + `}}`
}

const notTrainedReply = `{"status":"refused","schema_version":1,"refusal":{"kind":"task_not_trained","task":"career.jd_triage","available":[]},"message":"not trained"}`

func newTestBackend(t *testing.T, socket string) *Backend {
	t.Helper()
	b, err := NewBackend(BackendConfig{App: "vani", Tasks: []string{triageTask}, Socket: socket, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBackendMapsAnAnswer(t *testing.T) {
	agent := startAgent(t, reply(okReply("remote", "", false)))
	b := newTestBackend(t, agent.path)
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	wm := ans.Slots["work_mode"]
	if ans.Backend != "lappi" || wm.Reading != decide.Answered || wm.Value != "remote" ||
		wm.Lappi == nil || wm.Lappi.Score != 0.8 || strings.Join(wm.Lappi.ConformalSet, ",") != "remote" {
		t.Fatalf("work_mode = %+v (backend %s)", wm, ans.Backend)
	}
	if s := ans.Slots["seniority"]; s.Reading != decide.Abstained || s.Detail != "noul" || s.Value != "" {
		t.Fatalf("noul slot = %+v", s)
	}

	// The request carries the exact question and options, and a hashed id.
	var req Request
	if err := json.Unmarshal(<-agent.lines, &req); err != nil {
		t.Fatal(err)
	}
	ctxBytes, _ := base64.StdEncoding.DecodeString(req.ContextB64)
	if req.Task != triageTask || req.Question != "Classify the posting." || string(ctxBytes) != string(triageQuestion().Context) ||
		len(req.Slots) != 2 || req.Slots[0].Type != SlotChoice || strings.Join(req.Slots[0].Options, ",") != "remote,hybrid,onsite,not_stated" {
		t.Fatalf("request = %+v", req)
	}
	if strings.Contains(req.ExampleID, "acme") || !strings.HasPrefix(req.ExampleID, "vani:career.jd_triage:") {
		t.Fatalf("example id %q leaks the record id or lacks the app prefix", req.ExampleID)
	}
}

func TestBackendDegradedIsAbstention(t *testing.T) {
	agent := startAgent(t, reply(okReply("remote", "senior", true)))
	ans, err := newTestBackend(t, agent.path).Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	for name, rd := range ans.Slots {
		if rd.Reading != decide.Abstained || rd.Detail != "degraded" || rd.Value != "" {
			t.Fatalf("%s = %+v, want degraded abstention", name, rd)
		}
	}
}

// The first task_not_trained refusal stops the task being served: the next
// ask does not reach the socket.
func TestBackendRemembersTaskNotTrained(t *testing.T) {
	agent := startAgent(t, reply(notTrainedReply))
	b := newTestBackend(t, agent.path)
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if rd := ans.Slots["work_mode"]; rd.Reading != decide.Refused || rd.Detail != KindTaskNotTrained {
		t.Fatalf("refusal = %+v", rd)
	}
	if b.Serves(triageTask) {
		t.Fatal("still serves an untrained task")
	}
	ans, err = b.Decide(context.Background(), triageQuestion())
	if err != nil || ans.Slots["work_mode"].Reading != decide.NotAsked {
		t.Fatalf("second ask: %+v err=%v", ans.Slots["work_mode"], err)
	}
	if n := agent.accepts.Load(); n != 1 {
		t.Fatalf("agent saw %d connections, want 1", n)
	}
}

func TestBackendNoAgentIsUnavailableNotAnError(t *testing.T) {
	b := newTestBackend(t, filepath.Join(shortDir(t), "none.sock"))
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatalf("absent agent returned an error: %v", err)
	}
	if rd := ans.Slots["seniority"]; rd.Reading != decide.Unavailable || rd.Detail != UnavailableSocketNotFound {
		t.Fatalf("reading = %+v", rd)
	}
	if !b.Serves(triageTask) {
		t.Fatal("an absent agent must not mark the task untrained")
	}
}

func TestBackendServesOnlyConfiguredTasks(t *testing.T) {
	agent := startAgent(t, reply(okReply("remote", "mid", false)))
	b := newTestBackend(t, agent.path)
	q := triageQuestion()
	q.Task = "career.other"
	ans, err := b.Decide(context.Background(), q)
	if err != nil || ans.Slots["work_mode"].Reading != decide.NotAsked || agent.accepts.Load() != 0 {
		t.Fatalf("unconfigured task: %+v err=%v accepts=%d", ans.Slots["work_mode"], err, agent.accepts.Load())
	}
}

func TestBackendRejectsInvalidQuestionAndConfig(t *testing.T) {
	b := newTestBackend(t, filepath.Join(shortDir(t), "none.sock"))
	q := triageQuestion()
	q.Slots[0].Options = []string{"only"}
	if _, err := b.Decide(context.Background(), q); err == nil {
		t.Fatal("invalid question accepted")
	}
	for name, cfg := range map[string]BackendConfig{
		"no app":       {Tasks: []string{triageTask}, Socket: "/tmp/x.sock"},
		"bad app":      {App: "Vani App", Tasks: []string{triageTask}, Socket: "/tmp/x.sock"},
		"no tasks":     {App: "vani", Socket: "/tmp/x.sock"}, // neither tasks nor routes
		"empty task":   {App: "vani", Tasks: []string{""}, Socket: "/tmp/x.sock"},
		"relative":     {App: "vani", Tasks: []string{triageTask}, Socket: "x.sock"},
		"long timeout": {App: "vani", Tasks: []string{triageTask}, Socket: "/tmp/x.sock", Timeout: time.Minute},
	} {
		if _, err := NewBackend(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A value the agent's parser accepted but this question does not offer can
// only arise if the reply's slots differ from the question's; it must still
// never pass as an answer.
func TestAnswerRefusesValuesOutsideTheOptions(t *testing.T) {
	q := triageQuestion()
	res := Result{Reading: ModelAnswered, Backend: "b", Slots: map[string]SlotAnswer{
		"work_mode": {Value: jsontext.Value(`"spaceship"`), Score: 0.9},
	}}
	ans := answer(q, res)
	if rd := ans.Slots["work_mode"]; rd.Reading != decide.Failed || rd.Value != "" {
		t.Fatalf("work_mode = %+v", rd)
	}
	if rd := ans.Slots["seniority"]; rd.Reading != decide.Failed || rd.Detail != "missing_slot" {
		t.Fatalf("seniority = %+v", rd)
	}
}

// startRoutingAgent answers each request by its task: answers maps a family
// to the value its single slot answers, refuse lists families refused as
// untrained. It records the tasks asked, in order.
func startRoutingAgent(t *testing.T, answers map[string]string, refuse map[string]bool) (string, *[]string, *sync.Mutex) {
	t.Helper()
	path := filepath.Join(shortDir(t), "r.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var mu sync.Mutex
	var asked []string
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }() // a fake agent's close error changes nothing the test checks
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var req Request
				if json.Unmarshal(line, &req) != nil || len(req.Slots) != 1 {
					return
				}
				mu.Lock()
				asked = append(asked, req.Task)
				mu.Unlock()
				var out string
				if refuse[req.Task] {
					out = `{"status":"refused","schema_version":1,"refusal":{"kind":"task_not_trained"},"message":"m"}`
				} else {
					v := answers[req.Task]
					out = `{"status":"ok","schema_version":1,"backend":"qd-metal/test","degraded":false,"slots":{"` + req.Slots[0].Name +
						`":{"value":"` + v + `","conformal_set":["` + v + `"],"score":0.7,"noul":false,"degraded":false}}}`
				}
				_, _ = conn.Write([]byte(out + "\n"))
			}()
		}
	}()
	return path, &asked, &mu
}

func triageRoutes() []Route {
	return []Route{
		{Task: triageTask, Slot: "work_mode", Family: "career.jd_work_mode", Question: "Where is the work done?"},
		{Task: triageTask, Slot: "seniority", Family: "career.jd_seniority", Question: "What level?"},
	}
}

func TestRoutedSlotsAskTheirOwnFamilies(t *testing.T) {
	path, asked, mu := startRoutingAgent(t, map[string]string{"career.jd_work_mode": "hybrid", "career.jd_seniority": "senior"}, nil)
	b, err := NewBackend(BackendConfig{App: "vani", Routes: triageRoutes(), Socket: path, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Serves(triageTask) || b.Serves("career.jd_work_mode") {
		t.Fatal("Serves must answer for the question task, not the families")
	}
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if wm := ans.Slots["work_mode"]; wm.Reading != decide.Answered || wm.Value != "hybrid" || wm.Lappi == nil {
		t.Fatalf("work_mode = %+v", wm)
	}
	if s := ans.Slots["seniority"]; s.Reading != decide.Answered || s.Value != "senior" {
		t.Fatalf("seniority = %+v", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(*asked, ",") != "career.jd_work_mode,career.jd_seniority" {
		t.Fatalf("asked %v", *asked)
	}
}

// An untrained family stops being asked; the other family keeps answering.
func TestRoutedNotTrainedIsPerFamily(t *testing.T) {
	path, asked, mu := startRoutingAgent(t, map[string]string{"career.jd_seniority": "mid"}, map[string]bool{"career.jd_work_mode": true})
	b, err := NewBackend(BackendConfig{App: "vani", Routes: triageRoutes(), Socket: path, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ans, err := b.Decide(context.Background(), triageQuestion())
		if err != nil {
			t.Fatal(err)
		}
		wantWM := decide.Refused
		if i == 1 {
			wantWM = decide.NotAsked
		}
		if wm := ans.Slots["work_mode"]; wm.Reading != wantWM || wm.Detail != KindTaskNotTrained {
			t.Fatalf("round %d work_mode = %+v", i, wm)
		}
		if s := ans.Slots["seniority"]; s.Reading != decide.Answered || s.Value != "mid" {
			t.Fatalf("round %d seniority = %+v", i, s)
		}
	}
	if !b.Serves(triageTask) {
		t.Fatal("one trained family left: still served")
	}
	mu.Lock()
	got := strings.Join(*asked, ",")
	mu.Unlock()
	if got != "career.jd_work_mode,career.jd_seniority,career.jd_seniority" {
		t.Fatalf("asked %s", got)
	}
}

// No agent: the first slot reads socket_not_found and the rest copy it
// without dialling again; a slot with no route is not asked.
func TestRoutedAbsentAgentDialsOnce(t *testing.T) {
	routes := triageRoutes()[:1] // seniority has no route
	b, err := NewBackend(BackendConfig{App: "vani", Routes: append(routes, Route{Task: triageTask, Slot: "x", Family: "career.x", Question: "q"}),
		Socket: filepath.Join(shortDir(t), "none.sock"), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if wm := ans.Slots["work_mode"]; wm.Reading != decide.Unavailable || wm.Detail != UnavailableSocketNotFound {
		t.Fatalf("work_mode = %+v", wm)
	}
	if s := ans.Slots["seniority"]; s.Reading != decide.NotAsked || s.Detail != "no_route" {
		t.Fatalf("seniority = %+v", s)
	}
}

func TestRouteConfigRules(t *testing.T) {
	r := triageRoutes()
	for name, cfg := range map[string]BackendConfig{
		"empty field":      {App: "vani", Routes: []Route{{Task: triageTask, Slot: "a", Family: "f"}}},
		"direct and route": {App: "vani", Tasks: []string{triageTask}, Routes: r},
		"family twice":     {App: "vani", Routes: []Route{r[0], {Task: triageTask, Slot: "other", Family: r[0].Family, Question: "q"}}},
		"slot twice":       {App: "vani", Routes: []Route{r[0], {Task: triageTask, Slot: r[0].Slot, Family: "career.y", Question: "q"}}},
	} {
		cfg.Socket = "/tmp/x.sock"
		if _, err := NewBackend(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A hung agent costs one exchange timeout per question, not one per slot.
func TestRoutedHungAgentCostsOneTimeout(t *testing.T) {
	agent := startAgent(t, func(net.Conn) { time.Sleep(2 * time.Second) })
	b, err := NewBackend(BackendConfig{App: "vani", Routes: triageRoutes(), Socket: agent.path, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ans, err := b.Decide(context.Background(), triageQuestion())
	if err != nil {
		t.Fatal(err)
	}
	for name, rd := range ans.Slots {
		if rd.Reading != decide.Unavailable || rd.Detail != UnavailableDeadline {
			t.Fatalf("%s = %+v", name, rd)
		}
	}
	if n := agent.accepts.Load(); n != 1 {
		t.Fatalf("agent dialled %d times, want 1", n)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("question took %v", d)
	}
}
