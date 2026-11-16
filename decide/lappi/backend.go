package lappi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bharathvbcr/decide"
)

// KindTaskNotTrained is the refusal kind for a family the loaded release did
// not train (fixtures/wire/refusal-task_not_trained.json).
const KindTaskNotTrained = "task_not_trained"

// DefaultTimeout bounds one exchange when Config.Timeout is zero. The first
// request after the agent starts loads the model (about 3.4 s), so a shorter
// deadline would read every cold start as Unavailable.
const DefaultTimeout = 5 * time.Second

// BackendConfig configures the decide.Backend over a Lappi agent.
type BackendConfig struct {
	// App names the caller in example ids ("vani", "devcouncil").
	App string
	// Tasks are question tasks asked as they are: the task is the trained
	// family and the question's slots are the family's slots.
	Tasks []string
	// Routes send each slot of a multi-slot question to its own single-slot
	// family, with that family's exact trained question. Lappi's training
	// data carries one choice slot per example, so a caller whose other
	// backends take all slots at once (Jev, an LLM) routes them here.
	// Anything neither in Tasks nor routed is not served.
	Routes []Route
	// Socket is the agent's socket; empty resolves with SocketPath.
	Socket string
	// Timeout bounds one exchange, in (0, MaxExchangeTimeout]; zero is
	// DefaultTimeout.
	Timeout time.Duration
}

// Route maps one slot of a caller's question onto a trained family.
type Route struct {
	Task     string // the caller's question task, e.g. "career.jd_triage"
	Slot     string // the slot within it, e.g. "work_mode"
	Family   string // the trained family, e.g. "career.jd_work_mode"
	Question string // the family's exact trained question
}

// Backend asks a Lappi agent and maps its reply onto decide readings. It
// never grants anything: a value is one of the caller's options or nothing.
//
// Invariant 3 of the plan ("don't ask Lappi what it can't answer"): there is
// no wire op that lists trained families, so the first task_not_trained
// refusal for a task is remembered and that task is not served again for the
// life of the process. Restarting the caller after a new release re-probes.
type Backend struct {
	app    string
	client *Client
	tasks  map[string]bool
	routes map[string]map[string]Route // question task -> slot -> route

	mu         sync.Mutex
	notTrained map[string]bool // keyed by the family asked
}

// NewBackend validates cfg and builds the backend. It does not dial: an
// absent agent is an Unavailable reading at ask time, not a startup error.
func NewBackend(cfg BackendConfig) (*Backend, error) {
	if cfg.App == "" || !kindPattern.MatchString(cfg.App) {
		return nil, fmt.Errorf("lappi: app name %q must match %s", cfg.App, kindPattern)
	}
	if len(cfg.Tasks) == 0 && len(cfg.Routes) == 0 {
		return nil, errors.New("lappi: no tasks or routes configured")
	}
	sock := cfg.Socket
	if sock == "" {
		var err error
		if sock, err = SocketPath(os.Getenv); err != nil {
			return nil, err
		}
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	c, err := NewClient(sock, timeout)
	if err != nil {
		return nil, err
	}
	tasks := make(map[string]bool, len(cfg.Tasks))
	for _, t := range cfg.Tasks {
		if t == "" {
			return nil, errors.New("lappi: empty task name")
		}
		tasks[t] = true
	}
	routes := map[string]map[string]Route{}
	families := map[string]bool{}
	for _, r := range cfg.Routes {
		switch {
		case r.Task == "" || r.Slot == "" || r.Family == "" || r.Question == "":
			return nil, fmt.Errorf("lappi: route %+v has an empty field", r)
		case tasks[r.Task]:
			return nil, fmt.Errorf("lappi: task %q is both asked directly and routed", r.Task)
		case families[r.Family]:
			return nil, fmt.Errorf("lappi: family %q is routed twice", r.Family)
		}
		if routes[r.Task] == nil {
			routes[r.Task] = map[string]Route{}
		}
		if _, dup := routes[r.Task][r.Slot]; dup {
			return nil, fmt.Errorf("lappi: slot %s of %s is routed twice", r.Slot, r.Task)
		}
		routes[r.Task][r.Slot] = r
		families[r.Family] = true
	}
	return &Backend{app: cfg.App, client: c, tasks: tasks, routes: routes, notTrained: map[string]bool{}}, nil
}

// Name implements decide.Backend.
func (b *Backend) Name() string { return "lappi" }

// Serves implements decide.Backend: a direct task the agent has not refused
// as untrained, or a routed task with at least one such family.
func (b *Backend) Serves(task string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tasks[task] {
		return !b.notTrained[task]
	}
	for _, r := range b.routes[task] {
		if !b.notTrained[r.Family] {
			return true
		}
	}
	return false
}

func (b *Backend) trained(family string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.notTrained[family]
}

// Decide implements decide.Backend. Every outcome is a reading; the error is
// non-nil only for an invalid question, so an absent agent never looks like a
// fault in the caller.
func (b *Backend) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	if err := decide.ValidateQuestion(q); err != nil {
		return decide.Answer{}, err
	}
	if !b.Serves(q.Task) {
		return all(q, decide.SlotReading{Reading: decide.NotAsked, Detail: "not_served"}), nil
	}
	if b.tasks[q.Task] {
		return b.ask(ctx, q.Task, q.Prompt, q), nil
	}

	// Routed: one single-slot request per slot, in the question's order, all
	// under ctx. An agent that is not there is not dialled again for the
	// remaining slots.
	out := decide.Answer{Backend: "lappi", Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	var down *decide.SlotReading
	for _, s := range q.Slots {
		r, ok := b.routes[q.Task][s.Name]
		switch {
		case !ok:
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.NotAsked, Detail: "no_route"}
			continue
		case !b.trained(r.Family):
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.NotAsked, Detail: KindTaskNotTrained}
			continue
		case down != nil:
			out.Slots[s.Name] = *down
			continue
		}
		sub := q
		sub.Slots = []decide.Slot{s}
		rd := b.ask(ctx, r.Family, r.Question, sub).Slots[s.Name]
		out.Slots[s.Name] = rd
		// Absent, refusing or hung: the rest of this question would wait the
		// same way, so one exchange timeout bounds the whole question.
		if rd.Reading == decide.Unavailable && (rd.Detail == UnavailableSocketNotFound ||
			rd.Detail == UnavailableConnectRefused || rd.Detail == UnavailableDeadline) {
			down = &rd
		}
	}
	return out, nil
}

// ask sends one request for family with question and q's slots, and maps the
// reply. A task_not_trained refusal is remembered for family.
func (b *Backend) ask(ctx context.Context, family, question string, q decide.Question) decide.Answer {
	slots := make([]SlotSpec, len(q.Slots))
	for i, s := range q.Slots {
		slots[i] = SlotSpec{Name: s.Name, Type: SlotChoice, Options: s.Options}
	}
	res := b.client.Ask(ctx, NewRequest(b.app, family, question, q.Context, slots, exampleID(q.RecordID)))
	switch res.Reading {
	case ModelAnswered, ModelAbstained:
		return answer(q, res)
	case RequestRefused:
		if res.Kind == KindTaskNotTrained {
			b.mu.Lock()
			b.notTrained[family] = true
			b.mu.Unlock()
		}
		return all(q, decide.SlotReading{Reading: decide.Refused, Detail: res.Kind})
	case BackendFailed:
		return all(q, decide.SlotReading{Reading: decide.Failed, Detail: res.Kind})
	case Unavailable:
		return all(q, decide.SlotReading{Reading: decide.Unavailable, Detail: res.Kind})
	default:
		return all(q, decide.SlotReading{Reading: decide.NotAsked, Detail: res.NotAskedReason})
	}
}

// answer maps an ok reply. A degraded reply (a non-model backend answered) is
// an abstention for every slot; a noul slot is an abstention; a value outside
// the caller's options is Failed, never passed on.
func answer(q decide.Question, res Result) decide.Answer {
	out := decide.Answer{Backend: "lappi", Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	for _, s := range q.Slots {
		sa, ok := res.Slots[s.Name]
		switch {
		case !ok:
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.Failed, Detail: "missing_slot"}
		case res.Reading == ModelAbstained && !sa.Noul:
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.Abstained, Detail: "degraded"}
		case sa.Noul:
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.Abstained, Detail: "noul"}
		default:
			v, isChoice := sa.Choice()
			if !isChoice || decide.CheckValue(s, v) != nil {
				out.Slots[s.Name] = decide.SlotReading{Reading: decide.Failed, Detail: "unknown_choice"}
				continue
			}
			out.Slots[s.Name] = decide.SlotReading{
				Reading: decide.Answered,
				Value:   v,
				Lappi:   &decide.LappiCalib{Score: sa.Score, ConformalSet: append([]string(nil), sa.ConformalSet...)},
			}
		}
	}
	return out
}

func all(q decide.Question, rd decide.SlotReading) decide.Answer {
	out := decide.Answer{Backend: "lappi", Slots: make(map[string]decide.SlotReading, len(q.Slots))}
	for _, s := range q.Slots {
		out.Slots[s.Name] = rd
	}
	return out
}

// exampleID hashes the caller's record id so a posting key or file path never
// reaches the agent's logs; an empty id gets a fresh random one.
func exampleID(recordID string) string {
	if recordID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err == nil {
			return hex.EncodeToString(b[:])
		}
		return "unknown"
	}
	sum := sha256.Sum256([]byte(recordID))
	return hex.EncodeToString(sum[:16])
}
