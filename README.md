# herdr-autopin

English | [简体中文](README.zh-CN.md)

Watches every agent in [Herdr](https://herdr.dev) and, each time one stops, asks one thing:
**is this turn waiting for my decision?** If it is, the terminal gets the label `herdr.pinned=1`,
which keeps the agent at the top of Herdr's lists until you deal with it.

Herdr turns an agent from `done` to `idle` as soon as you look at it, whether or not you decided
anything. The pin is the mark that stays.

The question is answered by Jev, [TypeSafe AI](https://docs.typesafe.ai/introduction/quickstart)'s
System One model: cheap, and about a second per call. Nothing in herdr-autopin knows what an agent's
screen looks like, so it works with any agent that reports a status to Herdr (tested with pi and codex).

```text
herdr-core ──unix socket──> herdr-autopin ──HTTPS──> api.typesafe.ai/v1/systemone
  status events          screen before + after        blocked_on_user, asked_question
```

> **What leaves your machine.** Each time an agent stops, the last 60 non-blank lines of its
> terminal, before and after the turn, are sent to `api.typesafe.ai`. That can include code or
> secrets shown on screen. Use `-only` to limit which terminals are watched, and `-dry` to see
> what it would do first.

## Use

Needs Go 1.25+, a running herdr-core, and a TypeSafe API key.

```sh
export TYPESAFE_API_KEY=...
go build -o herdr-autopin ./cmd/herdr-autopin
./herdr-autopin            # watch and pin
./herdr-autopin -dry       # watch and judge, never pin
./herdr-autopin -judge t7  # judge one terminal once, without pinning (no "before" screen: reads high)
./herdr-autopin -judge - < screens.json  # judge two saved screens: {"base":[lines],"now":[lines]}
```

What it logs:

```text
22:17:49 t8z  working -> done  "file count"
22:17:54 t8z  jev blocked=0.07 asked=0.04  not mine
22:18:07 t8z  working -> done  "merge plan"
22:18:10 t8z  jev blocked=0.74 asked=0.94  PINNED
```

`make dev` builds it and restarts it in the background, logging to `/tmp/herdr-autopin.log`.

## Install as a service

macOS. The installer drops the binary in `~/.local/bin` and runs it as a LaunchAgent
(`~/Library/LaunchAgents/com.tomyail.herdr-autopin.plist`): it starts at login and restarts
when it exits.

```sh
curl -fsSL https://raw.githubusercontent.com/Tomyail/herdr-autopin/main/install.sh \
  | TYPESAFE_API_KEY=... sh
```

From a checkout, `./install.sh --key ...` does the same. Flags the service should carry go after
`--`: `./install.sh --dry -- --only herdr.space=<id>`.

```sh
./install.sh --uninstall            # stop it, keep the binary
./install.sh --uninstall --purge    # and delete the binary
launchctl print gui/$(id -u)/com.tomyail.herdr-autopin   # is it up
tail -f ~/Library/Logs/herdr-autopin.log                 # what it does
```

The plist holds your API key, so it is chmod 600. Reinstalling replaces it. Prebuilt tarballs
(darwin arm64/amd64) come from GitHub Releases; with no release yet the installer builds from source
when Go is installed.

## Options

| Flag | Default | What it does |
|---|---|---|
| `-blocked` | `0.5` | Pin when `blocked_on_user` reaches this |
| `-asked` / `-asked-floor` | `0.6` / `0.25` | Or when `asked_question` reaches `-asked` and `blocked_on_user` is at least `-asked-floor` |
| `-label k=v` | `herdr.pinned=1` | The label set on an agent that waits for you; use your own to leave pins alone |
| `-only k=v` | every terminal | Watch only terminals carrying this label, repeatable (`-only herdr.space=<id>`). Core does the filtering: other terminals' screens are never read |
| `-tail` | `60` | Lines of each screen sent to Jev (the last N after dropping blank lines) |
| `-jev-url` / `-jev-model` | `https://api.typesafe.ai/v1/systemone` / `jev-latest` | Jev's endpoint and model |
| `-dry` | off | Watch and judge, log only, set no label |

Environment: `TYPESAFE_API_KEY` (required), `HC_SESSION` (herdr-core's session, default `default`).

The default thresholds come from a handful of samples. Run with `-dry` for a while and compare
the scores in the log with what you would have decided, then adjust.

## How it decides

1. It subscribes to herdr-core's events and acts when an agent's status becomes `done` or `blocked`.
2. An agent that says why it is blocked (a permission, a question: `status.kind`) is pinned at once.
   No screen is read and Jev is not called.
3. Otherwise it reads the terminal and sends Jev two screens: `screen_before`, how the terminal
   looked before this turn, and `screen_after`, how it looks now. Jev tells the turn's text from
   the interface around it. One screen alone is not enough: footers, banners and update notices
   read as things waiting for you.
4. One call asks two yes/no questions, each answered with a probability:

| Question | Meaning |
|---|---|
| `blocked_on_user` | Work cannot continue until the user answers or decides |
| `asked_question` | The agent's final message asks the user something or offers options |

The "before" screen is kept in memory per terminal. It is read each time the agent rests
(`idle`, `done`, `blocked`), once two reads 0.4 s apart agree apart from digits. An agent started
with its task, which never rested, uses its screen as it starts `working`. With neither (herdr-autopin
started in the middle of the turn), that turn is not judged.

## Agents on other machines

They work with no extra setup. The local core lists terminals of the machines it is connected to,
addressed as `machine/t7`; events, reads and labels all take those addresses, and the label is
written on that machine's core, so every Herdr client sees it.

```text
17:37:47 mini/t3t  working -> done  "Exact OK reply"
17:37:51 mini/t3t  jev blocked=0.26 asked=0.05  not mine
```

## Two things about herdr-core's socket

1. **A connection that subscribed stops answering requests.** herdr-autopin opens two: one for
   events, one for reads and labels.
2. **A status is an object**, `{"state":"done","app":"pi","msg":"..."}`, not a string.

When core goes away (an update, a restart), herdr-autopin retries every 3 seconds until it is back.

## Limits

- It never removes a pin. Unpin in Herdr once you have decided.
- A turn already running when herdr-autopin starts has no "before" screen and is not judged.
- The same turn can be judged twice.
- Core's `missed` event (sent to a subscriber that fell behind) is ignored, so one status change
  can be lost after it.
- Not tested with replies longer than `-tail` lines, or with agents other than pi and codex.

## License

MIT
