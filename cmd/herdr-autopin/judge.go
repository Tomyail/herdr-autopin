package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Jev is TypeSafe's System One model: state + typed questions -> typed answers.
// https://docs.typesafe.ai/introduction/quickstart
var (
	jevURL   = flag.String("jev-url", "https://api.typesafe.ai/v1/systemone", "Jev (System One) endpoint")
	jevModel = flag.String("jev-model", "jev-latest", "Jev model")
)

// When the two questions disagree, either one is enough: a message that asks me
// something needs my answer even if the agent could carry on without it.
var (
	blockedPin = flag.Float64("blocked", 0.5, "pin when Jev is this sure the work stops until you decide")
	askedPin   = flag.Float64("asked", 0.6, "or this sure its last message asks you something...")
	askedFloor = flag.Float64("asked-floor", 0.25, "...on top of this much -blocked")
)

// Waits is the verdict on a finished agent: does it want me before it goes on.
func (v Verdict) Waits() bool {
	return v.Blocked >= *blockedPin || (v.Asked >= *askedPin && v.Blocked >= *askedFloor)
}

// Verdict is Jev's answer about one terminal.
type Verdict struct {
	Blocked float64 // probability that work waits for the user
	Asked   float64 // probability that the agent's last message asks the user
}

// tail is how much of each screen Jev gets: the turn's answer is at the end.
var tail = flag.Int("tail", 60, "lines of each screen sent to Jev (blank lines dropped)")

// lastLines drops blank lines and keeps the end of a screen.
func lastLines(text string, n int) string {
	kept := make([]string, 0, n)
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimRight(line, " \t"); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// Judge asks Jev whether the agent's latest turn waits for the user. It gets the
// screen before the turn and the screen after it and tells the turn's text from
// the interface itself: nothing here knows what any agent's screen looks like.
func Judge(before, after string) (Verdict, error) {
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		return Verdict{}, errors.New("TYPESAFE_API_KEY is not set")
	}

	payload := map[string]any{
		"model": *jevModel,
		"state": map[string]any{
			"screen_before": lastLines(before, *tail),
			"screen_after":  lastLines(after, *tail),
		},
		"questions": map[string]any{
			"blocked_on_user": map[string]any{
				"type": "noul",
				"instructions": "screen_before is the terminal before the agent's latest turn, " +
					"screen_after is the terminal after it. Looking only at what the agent wrote " +
					"in that latest turn (the text present in screen_after but not in screen_before), " +
					"work cannot continue until the user answers or decides.",
			},
			"asked_question": map[string]any{
				"type": "noul",
				"instructions": "Looking only at what is new in screen_after compared to " +
					"screen_before, the agent's final message asks the user a question or " +
					"offers options to pick between.",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Verdict{}, err
	}
	log.Printf("POST %s (%d bytes)", *jevURL, len(body))

	req, err := http.NewRequest(http.MethodPost, *jevURL, bytes.NewReader(body))
	if err != nil {
		return Verdict{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Verdict{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out struct {
		Answers map[string]struct {
			Noul float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Verdict{}, fmt.Errorf("jev: bad response: %w", err)
	}
	blocked, ok := out.Answers["blocked_on_user"]
	if !ok {
		return Verdict{}, fmt.Errorf("jev: no blocked_on_user answer in %s", truncate(string(raw), 200))
	}
	return Verdict{Blocked: blocked.Noul, Asked: out.Answers["asked_question"].Noul}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
