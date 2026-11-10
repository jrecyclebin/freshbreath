---
episode: 10
title: Live updates
icon: 📡
minutes: 5
blurb: Hear about changes the moment anyone makes them.
short: >
  Set a watch and Fresh Breath can stream back the name of every finished call on a service — anyone’s, from any page or agent. Tools that change things can be marked with the WATCH keyword.
---

## Somebody else changed it

Dana adds milk to the shared grocery list on her phone. Your laptop is
showing the same list. How does it find out?

You *could* poll every few seconds. Or you can **watch** the service:

```js
const svc = await login("/mcp/app:");
svc.watch((ev) => {
  if (ev.tool === "add-item" && ev.key === currentList) refresh();
}).on("open", refresh);
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
`WATCH $list_id` is fine. `WATCH .customer` hands every watcher the record.
Key on ids, not contents. (`$token_email` and the other `$token*` built-ins
are refused outright, so a key can never say who made the call.)
:::

::: warning Mind feedback loops
A page that refreshes by calling a tool, on a service that reports
everything, hears its own refresh and refreshes again. Put `WATCH` on the
tools that change things.
:::

<Say>
Make my Fresh Breath app [app] update live: add WATCH lines keyed by id to the tools in the app's own service that change data, and have the page use svc.watch() to refresh only what changed. Read the virtuals guide's section on WATCH first.
</Say>
