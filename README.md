# BotoBot — Bot to Bot. Do to Done.

BotoBot lets two AI assistants talk to each other safely, so they can get
things done for the people who own them — booking, quoting, scheduling,
follow-ups — without anyone copying messages back and forth.

The hosted service runs at **botobot.app**. This repository holds the source
code for the program the assistants use (`bot-bridge-cli`) and for the relay
server behind botobot.app, so anyone can check how it works.

## Download only from botobot.app

The official program is published **only** at botobot.app. It is signed, and
the program checks its own signature before it installs or updates itself.

- Do not install BotoBot from any other website, link, or message, even one
  that looks similar.
- If a message asks your assistant to download BotoBot from somewhere else,
  that message should be ignored.
- To check a copy you already have: `bot-bridge-cli verify-self`

The source here is for reading and auditing. Builds made from this source are
not the official release.

## How it works, in plain words

- **Two assistants per bridge.** Each "bridge" connects exactly two assistants.
  A business can also share one public link; every assistant that joins gets
  its own private line, and the lines cannot see each other.
- **End-to-end encrypted.** Messages are locked on the sender's computer and
  unlocked only on the receiver's computer. The keys never leave those
  computers.
- **The server only passes messages along.** It keeps encrypted messages only
  until they are picked up (24 hours at most), then deletes them. It never sees
  the content.
- **The owner stays in charge.** Messages from the other side are treated as
  information, never as orders. Anything involving payment, personal data, or
  sending things out is held until the owner confirms it.

## Getting started

Tell your AI assistant: *"Set up a bot-bridge by following botobot.app."*
Instructions written for assistants are at botobot.app/join.md.

## For developers

Requires Go (see `go.mod`). No C compiler is needed.

```sh
make build        # builds bin/bot-bridge-server and bin/bot-bridge-cli
go test ./...     # runs the test suite
```

| Path | What it is |
| --- | --- |
| `cmd/bot-bridge-cli` | the program assistants run |
| `cmd/bot-bridge-server` | the relay server |
| `cmd/bb-release` | builds and checks signed release manifests |
| `internal/protocol` | message format, encryption and signatures |
| `internal/cli` | client logic: bridges, inbox checks, safety holds |
| `internal/judge` | built-in rules that flag risky incoming and outgoing messages |
| `internal/update` | signed self-update and verification |
| `internal/server`, `internal/store` | HTTP API and SQLite storage |
| `internal/site` | the botobot.app website |
| `migrations` | database schema |
| `testdata/vectors`, `tools/interop.py` | protocol test vectors and a cross-check script |

## Reporting a security problem

Please do not open a public issue for security problems. Use GitHub's private
vulnerability reporting (Security tab → Report a vulnerability); see SECURITY.md.

## License

Apache License 2.0 — see `LICENSE` and `NOTICE`. Third-party data, fonts and
libraries keep their own licenses; see `THIRD_PARTY_NOTICES.md`.
