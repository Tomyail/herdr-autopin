// herdr-autopin follows agent states on the herdr-core socket and pins the ones
// that are waiting for a decision from the user.
//
//	herdr-core <-unix socket-> herdr-autopin -> api.typesafe.ai (Jev)
//
// Build: go build ./cmd/herdr-autopin   Run: TYPESAFE_API_KEY=... ./herdr-autopin
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// screens keeps each terminal's last still screen: what it showed before the
// turn that is being judged.
type screens struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *screens) get(t string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[t]
}

func (s *screens) set(t, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[t] = text
}

var shown = &screens{m: map[string]string{}}

// version is stamped at build time: -ldflags "-X main.version=...".
var version = "dev"

var digits = regexp.MustCompile(`\d+`)

// read returns the terminal's screen once it holds still: two reads that agree
// apart from counters (a clock, a token count). A screen that never settles is
// returned as it is.
func read(c *Core, terminal string) (string, error) {
	var prev string
	for i := 0; ; i++ {
		var r ReadResult
		if err := c.Call("terminal.read", map[string]any{"terminal": terminal, "lines": 200}, &r); err != nil {
			return "", err
		}
		now := digits.ReplaceAllString(r.Text, "#")
		if (i > 0 && now == prev) || i == 5 {
			return r.Text, nil
		}
		prev = now
		time.Sleep(400 * time.Millisecond)
	}
}

// remember stores the terminal's screen as the one before its next turn.
func remember(c *Core, terminal string) {
	if text, err := read(c, terminal); err == nil {
		shown.set(terminal, text)
	}
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("herdr-autopin  ")

	showVersion := flag.Bool("version", false, "print the version and exit")
	// -judge t7: run one judgment and print it, without pinning.
	judgeOnly := flag.String("judge", "", "judge this terminal once and exit (-: {\"base\":[lines],\"now\":[lines]} on stdin)")
	dry := flag.Bool("dry", false, "watch and judge, but never pin")
	flag.Func("label", "label set on an agent that waits for you, key=value (default herdr.pinned=1)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return fmt.Errorf("want key=value")
		}
		pinLabel = map[string]string{k: v}
		return nil
	})
	only := map[string]string{}
	flag.Func("only", "watch only terminals carrying this label, key=value (repeatable; default: all)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return fmt.Errorf("want key=value")
		}
		only[k] = v
		return nil
	})
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *judgeOnly == "-" { // two saved screens, for trying the judgment out
		var in struct{ Base, Now []string }
		if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
			log.Fatal(err)
		}
		v, err := Judge(strings.Join(in.Base, "\n"), strings.Join(in.Now, "\n"))
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("blocked=%.2f asked=%.2f waits=%v", v.Blocked, v.Asked, v.Waits())
		return
	}
	if *judgeOnly != "" {
		c, err := Dial()
		if err != nil {
			log.Fatal(err)
		}
		if err := c.Hello(); err != nil {
			log.Fatal(err)
		}
		// No screen from before the turn: Jev sees the whole screen as the turn,
		// so the interface counts too and this reads high.
		shown.set(*judgeOnly, " ")
		check(c, *judgeOnly, Status{}, false)
		return
	}

	// Core restarts (an update, a reboot): keep trying until it is back. Only the
	// first failure is logged, so a long outage doesn't fill the log.
	for down := false; ; time.Sleep(3 * time.Second) {
		err := watch(*dry, only)
		if !down || !errors.Is(err, errDial) {
			log.Printf("core connection lost: %v; retrying every 3s", err)
		}
		down = errors.Is(err, errDial)
	}
}

// watch follows core's events until the connection ends.
func watch(dry bool, only map[string]string) error {
	c, err := Dial()
	if err != nil {
		return err
	}
	defer c.Close()
	// Two connections: once a connection subscribes, core stops answering
	// requests on it (a subscription owns the connection).
	calls, err := Dial()
	if err != nil {
		return err
	}
	defer calls.Close()
	if err := c.Hello(); err != nil {
		return err
	}
	if err := calls.Hello(); err != nil {
		return err
	}
	var sub struct {
		Terminals []Terminal `json:"terminals"`
	}
	if err := c.Call("events.subscribe", map[string]any{"labels": only}, &sub); err != nil {
		return err
	}

	last := map[string]string{} // terminal -> last state we saw
	for _, t := range sub.Terminals {
		if t.Status != nil {
			last[t.Address] = t.Status.State
		}
	}
	log.Printf("watching %d terminals, %d with an agent", len(sub.Terminals), len(last))
	go func(resting map[string]string) { // what resting agents show now is where their next turn starts
		for t, state := range resting {
			if state != "working" {
				remember(calls, t)
			}
		}
	}(maps.Clone(last))

	for ev := range c.Events {
		if ev.Type != "status" {
			continue
		}
		var d struct {
			Status *Status `json:"status"`
		}
		if json.Unmarshal(ev.Data, &d) != nil || d.Status == nil {
			continue
		}
		prev, seen := last[ev.Terminal]
		last[ev.Terminal] = d.Status.State
		if seen && prev == d.Status.State {
			continue
		}
		log.Printf("%s  %s -> %s  %q", ev.Terminal, prev, d.Status.State, d.Status.Msg)

		switch d.Status.State {
		case "done", "blocked":
			go check(calls, ev.Terminal, *d.Status, !dry)
		case "idle":
			go remember(calls, ev.Terminal)
		case "working":
			// An agent started with its task never rested: its screen as the turn
			// begins is the best "before" there is.
			if shown.get(ev.Terminal) == "" {
				go remember(calls, ev.Terminal)
			}
		}
	}
	return errors.New("core closed the connection")
}

// check pins the agent when it waits for the user. A program that says why it
// stopped (kind) is believed; otherwise its screen is read and Jev is asked.
func check(c *Core, terminal string, st Status, pin bool) {
	// kind is exact, free, and needs no screen scraping.
	if st.Kind != "" && pin {
		pinNow(c, terminal, "kind="+st.Kind)
		return
	}
	if !pin && st.Kind != "" {
		log.Printf("%s  kind=%s  PINNABLE", terminal, st.Kind)
		return
	}

	after, err := read(c, terminal)
	if err != nil {
		log.Printf("%s  read failed: %v", terminal, err)
		return
	}
	before := shown.get(terminal)
	shown.set(terminal, after)
	if before == "" {
		log.Printf("%s  no screen from before this turn  not judged", terminal)
		return
	}

	v, err := Judge(before, after)
	if err != nil {
		log.Printf("%s  judge failed: %v", terminal, err)
		return
	}

	if !v.Waits() {
		log.Printf("%s  jev blocked=%.2f asked=%.2f  not mine", terminal, v.Blocked, v.Asked)
		return
	}
	if !pin {
		log.Printf("%s  jev blocked=%.2f asked=%.2f  PINNABLE", terminal, v.Blocked, v.Asked)
		return
	}
	pinNow(c, terminal, fmt.Sprintf("jev blocked=%.2f asked=%.2f", v.Blocked, v.Asked))
}

// pinLabel is what marks an agent as waiting; Herdr lists herdr.pinned first.
var pinLabel = map[string]string{"herdr.pinned": "1"}

// pinNow marks the agent as waiting for a decision.
func pinNow(c *Core, terminal, because string) {
	err := c.Call("terminal.label", map[string]any{
		"terminal": terminal,
		"labels":   pinLabel,
	}, nil)
	if err != nil {
		log.Printf("%s  pin failed: %v", terminal, err)
		return
	}
	log.Printf("%s  %s  PINNED", terminal, because)
}
