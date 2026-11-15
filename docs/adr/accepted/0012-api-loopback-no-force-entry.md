# ADR-0012: Bind the API to loopback and disable force entries

- Status: accepted
- Date: 2026-10-30
- Code: `docker-compose.yml` `ports`, `user_data/config.json` `force_entry_enable`

## Context

The API port was published on 0.0.0.0 and [::], so the trading API was reachable from
outside the host, behind a single password.

## Decision

The port binds `127.0.0.1`. The watchdog reaches the bot over the compose network and
`bin/status.sh` over localhost. FreqUI from another machine goes through
`ssh -L 8989:127.0.0.1:8989`. `force_entry_enable` is false; it came in as true from
upstream's exampleconfig and let anyone holding the password open positions.

