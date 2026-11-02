---
name: cardex-dispatch
description: Operate Cardex v2 from a Hermes or Codex management session. Use when creating objectives or cards, checking status, answering a question, approving a positioning proposal, or handing a decision to Otto.
---

# Cardex dispatch

This skill covers `--origin`, answering a question, approving a positioning proposal, and handing a decision to Otto. Use it from a Hermes or Codex management session. Point every command at the data directory with `--home` or `CARDEX_HOME`.

## Create with --origin

`objective add` and `card add` record an origin of the form `kind:id`. Pass `--origin` on the command. A non-empty `--origin` wins over `CARDEX_ORIGIN`. Both sides of the colon must be non-empty; the id is everything after the first colon.

```
cardex objective add --project p --title 账单 --mode auto --freeze-mode fast --origin hermes:session
cardex card add --objective o --title 实现 --kind dev --origin hermes:session
```

The objective stores origin on the objective, and `objective show` prints it. The card stores origin as an `origin` event, and `card show` prints it back.

## View status

`objective list` and `objective show` for the objective. `card list` and `card show` for cards. `question list` and `question show` for open questions. `node list` and `node show` for design nodes. `channel list` and `channel show` for channels. `route explain <card>` prints the route already stored on that card. `doctor` reports channel, window, and positioning problems.

## Answering a question

Answering a question uses `question answer <id> <option>`. Read the stored options with `question show` first. The option must already be on that question; an empty answer is refused. Submit only an option that was already chosen.

## Approving a positioning proposal

Approving a positioning proposal uses `positioning approve <id>` after `positioning proposals` and `positioning show`. Approve only a proposal that was already chosen; it becomes the current table. `positioning reject <id>` returns an error and leaves the current table unchanged.

## Handing a decision to Otto

Handing a decision to Otto is required for a direction freeze, a budget exhaustion, a breaker, and an objective-done question, and for any positioning change this session was not told to apply. This session runs `question answer` or `positioning approve` only after that choice already exists. Do not pick the option in this session.
