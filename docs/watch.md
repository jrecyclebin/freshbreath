---
episode: 9
title: Live updates with watch()
icon: 📡
minutes: 5
blurb: Hear about changes the moment anyone makes them.
short: svc.watch() streams every finished call on a service — anyone’s, from any page or agent. Mark the tools that change things with WATCH, key the events by id, and pages refresh only when their data moved.
---

## Somebody else changed it

Dana adds milk to the shared grocery list on her phone. Your laptop is
showing the same list. How does it find out?

You *could* poll every few seconds. Or you can **watch** the service:

```js
const svc = await login("/mcp/app:");
svc.watch((ev) => {
  if (ev.tool === "add-item" && ev.key === currentList) refresh();
}, { onOpen: refresh });
```

Every successful call on the service — from any page, any agent, any
scheduled task — arrives as a small event: the tool's name, and a key if
the tool sets one. No arguments, no results.

## WATCH in the tool script

By default every call is reported, by name. Once **any** tool in the file
has a `WATCH` line, only the tools with one are:

```txt
[add-item] Add an item to a list.

INSERT INTO items (list_id, name)
  VALUES ($list_id, $name)
WATCH $list_id
---
[rename-list] Rename a list.

UPDATE lists SET name = $name
  WHERE id = $list_id
WATCH
---
[list-items] Read a list. Not watched: reads change nothing.

SELECT name FROM items
  WHERE list_id = $list_id
```

`WATCH $list_id` sends `{"tool": "add-item", "key": 12}`. A bare `WATCH`
sends just the name. A page showing list 12 refreshes on key 12 and ignores
the rest.

## See it live

Two pages, one service. Add things, switch lists, then try taking the WATCH
line out.

<WatchSim />

## Two warnings

::: warning A WATCH key is broadcast
Everyone who can open the app can watch it, and sees every caller's events.
`WATCH $list_id` is fine. `WATCH $token_email` tells every watcher who just
made the call. Key on ids, not contents.
:::

::: warning Mind feedback loops
A page that refreshes by calling a tool, on a service that reports
everything, hears its own refresh and refreshes again. Put `WATCH` on the
tools that change things.
:::

<Say>
Make my Fresh Breath app [app] (ID [nonce]) update live: add WATCH lines keyed by id to the tools in app:[nonce] that change data, and have the page use watch() to refresh only what changed. Read the virtuals guide's section on WATCH first.
</Say>

::: details Under the hood
`watch()` streams `/service/watch/{id}` and reconnects by itself. Events
missed while disconnected aren't replayed, so re-read in `onOpen`. Task
services report every successful task by name; proxied API services report
each fetch as `{"method": "POST", "fetch": "/v1/items/7"}`. Proxied MCP
services can't be watched. A `WATCH` after a response-shaping block never
runs, so put it before.
:::
