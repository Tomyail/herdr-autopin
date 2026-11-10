// Package main: herdr-autopin follows agent states on the herdr-core socket and
// pins the ones that are waiting for a decision from the user.
//
//	herdr-core <-unix socket, JSON-RPC lines-> herdr-autopin -> api.typesafe.ai (Jev)
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status is what an agent reports (OSC 7501).
type Status struct {
	State string `json:"state"` // working | blocked | idle | done | error
	App   string `json:"app"`
	Msg   string `json:"msg"`
	// Kind is why a blocked agent stopped: permission, question or auth. The
	// program says it itself (OSC 7501); nothing has to be guessed from its
	// screen. Empty when the program didn't say.
	Kind string `json:"kind"`
}

// Terminal is one core terminal (only the fields used here).
type Terminal struct {
	Address string  `json:"address"`
	Name    *string `json:"name"`
	Status  *Status `json:"status"`
	Turn    int     `json:"turn"`
	Cwd     string  `json:"cwd"`
}

// ReadResult is the answer of terminal.read.
type ReadResult struct {
	Text     string `json:"text"`
	Complete bool   `json:"complete"`
	How      string `json:"how"`
}

// Event is one "event" notification.
type Event struct {
	Type     string          `json:"type"`
	Terminal string          `json:"terminal"`
	Data     json.RawMessage `json:"data"`
}

type reply struct {
	raw json.RawMessage
	err error
}

// Core is a connection to herdr-core.
type Core struct {
	conn    net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	waiting map[int]chan reply
	Events  chan Event
}

func socketPath() string {
	session := os.Getenv("HC_SESSION")
	if session == "" {
		session = "default"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/herdr-core", session, session+".sock")
}

func (c *Core) Hello() error {
	return c.Call("hello", map[string]any{"generation": 1}, nil)
}

// errDial: core isn't there (not running, or restarting).
var errDial = errors.New("dial")

// Close ends the connection and its read loop.
func (c *Core) Close() { c.conn.Close() }

// Dial connects and starts the read loop. Call Hello first.
func Dial() (*Core, error) {
	conn, err := net.Dial("unix", socketPath())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDial, err)
	}
	c := &Core{conn: conn, waiting: map[int]chan reply{}, Events: make(chan Event)}
	go c.readLoop()
	return c, nil
}

func (c *Core) readLoop() {
	defer close(c.Events)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var f struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params Event           `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &f) != nil {
			continue
		}
		switch {
		case f.ID != nil: // reply to a Call
			c.mu.Lock()
			ch := c.waiting[*f.ID]
			delete(c.waiting, *f.ID)
			c.mu.Unlock()
			if ch == nil {
				continue
			}
			if f.Error != nil {
				ch <- reply{err: errors.New(f.Error.Message)}
			} else {
				ch <- reply{raw: f.Result}
			}
		case f.Method == "event":
			c.Events <- f.Params
		}
		// heartbeat, missed: nothing to do in this version
	}
}

// Call sends one JSON-RPC request and decodes its result into out (may be nil).
func (c *Core) Call(method string, params any, out any) error {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan reply, 1)
	c.waiting[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	_, err = c.conn.Write(append(msg, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return err
	}

	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("%s: %w", method, r.err)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(r.raw, out)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("%s: timeout", method)
	}
}
