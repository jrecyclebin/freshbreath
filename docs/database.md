---
episode: 7
title: Give it a database
icon: 🗄️
minutes: 6
blurb: Write tools as SQL, and call them from the app or your agent.
short: Every app has its own database and its own blank toolbox, its app service. You fill the toolbox with tools — each one a named SQL query — and the app calls them with callTool().
---

## Buttons, not spreadsheets

Your app never edits its database directly. Instead you write **tools**:
named, fixed actions like *add an issue* or *list open issues*. The app
fills in the blanks — a title, a status — but can't change what the tool
does.

::: info 📊 Think of it like spreadsheets with labeled buttons
The database is the spreadsheets. Each tool is a button that does one safe
thing. Nothing can be tricked into deleting the whole sheet, because there's
no button for that.
:::

Every app comes with an empty toolbox already: a virtual service named
`app:` plus its nonce. No setup, no linking. Just write tools into it.

## The tool script format

Tools live in one plain text file:

```txt
[add-issue] File a new issue.

INSERT INTO issues (title, added_by)
  VALUES ($title, $token_email)
---
[list-issues] List the issues with a status.

SELECT id, title FROM issues
  WHERE status = $status
```

1. `[tool-name] description` starts each tool.
2. `---` on its own line separates tools.
3. A SQL keyword at the start of a line starts a query; **every line after
   it is indented**.
4. Words starting with `$` are the blanks. They're bound safely, never
   pasted into the SQL.
5. `$token_email`, `$token_id` and `$token_sub` are filled in by Fresh
   Breath with the signed-in person.

## Write one yourself

This is a real SQLite database running in your browser, reading the real
format. Run the tools, then edit the script and see what breaks.

<ToolSim />

## Ask for it

You'll rarely write these by hand. Describe what to remember, the way you'd
plan spreadsheet columns, and watch your agent build the file.

<ChatSim title="Try it: Claude writes the tools" file="app:Tix · tools">

- @you I want Tix to save issues: a title, a status, and who filed it. Read the Fresh Breath virtuals guide first.
- @claude Reading `get_guide("virtuals")`… Every app has its own blank service, so I'll write into Tix's. First, a `migrate` tool to create the table. The app will run it when it opens.

  ```txt
  [migrate] Create the tables if they don't exist.

  CREATE TABLE IF NOT EXISTS issues (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT DEFAULT 'open',
    added_by TEXT)
  ```

- @claude Now adding and listing. I'll fill `added_by` from `$token_email`, so nobody can file an issue as someone else.

  ```txt
  [migrate] Create the tables if they don't exist.

  CREATE TABLE IF NOT EXISTS issues (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT DEFAULT 'open',
    added_by TEXT)
  ---
  [add-issue] File a new issue.

  INSERT INTO issues (title, added_by)
    VALUES ($title, $token_email)
  ---
  [list-issues] List issues with a status: open or closed.

  SELECT id, title, status, added_by
    FROM issues
    WHERE status = $status
    ORDER BY id DESC
  ```

- @you Nice. Can I search them too?
- @claude Sure, a `search-issues` tool… Hm, after writing it the tools won't load: “$term is inside a SQL string literal”. In `LIKE '%$term%'` a variable can't be bound. I'll take the whole pattern as the argument instead.

  ```txt
  [migrate] Create the tables if they don't exist.

  CREATE TABLE IF NOT EXISTS issues (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT DEFAULT 'open',
    added_by TEXT)
  ---
  [add-issue] File a new issue.

  INSERT INTO issues (title, added_by)
    VALUES ($title, $token_email)
  ---
  [list-issues] List issues with a status: open or closed.

  SELECT id, title, status, added_by
    FROM issues
    WHERE status = $status
    ORDER BY id DESC
  ---
  [search-issues] Find issues whose title matches a pattern, like %fern%.

  SELECT id, title, status
    FROM issues
    WHERE title LIKE $pattern
  ```

- @claude And a way to close them, so the list doesn't only grow. Then I'll wire the app's buttons to `callTool()`.

  ```txt
  [migrate] Create the tables if they don't exist.

  CREATE TABLE IF NOT EXISTS issues (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT DEFAULT 'open',
    added_by TEXT)
  ---
  [add-issue] File a new issue.

  INSERT INTO issues (title, added_by)
    VALUES ($title, $token_email)
  ---
  [list-issues] List issues with a status: open or closed.

  SELECT id, title, status, added_by
    FROM issues
    WHERE status = $status
    ORDER BY id DESC
  ---
  [search-issues] Find issues whose title matches a pattern, like %fern%.

  SELECT id, title, status
    FROM issues
    WHERE title LIKE $pattern
  ---
  [close-issue] Close an issue.

  UPDATE issues SET status = 'closed'
    WHERE id = $id
  ```

- @claude Done: five tools in Tix's service, and the page saves and loads through them. Add an issue at **/tix@dev**, then refresh. It should still be there.

</ChatSim>

<Say>
I want my Fresh Breath app [app] to save [what to save, e.g. issues with a title, status and who added them]. Read the Fresh Breath virtuals guide first. Then write tools into the app's own service: a migrate tool that creates the tables, plus tools to add, list, update and delete. Have the app run migrate when it opens and save and load through these tools.
</Say>

Then check from your agent's side:

<Say>
Look in my Fresh Breath app [app]'s database and show me what tables exist and what's saved so far.
</Say>

::: details The rules the parser enforces
- Indent every continuation line — `WHERE`, `FROM`, `ORDER BY`, and closing
  parentheses too. A query ends at the first un-indented line.
- Deletes are spelled `DELETE FROM`. That's how the parser tells them apart
  from an HTTP `DELETE`.
- No `$var` inside quotes: `LIKE '%$term%'` can't be bound. Write
  `LIKE $pattern` and pass `%frog%`.
- `$$` is a literal dollar sign.
- Queries stop after 5 seconds and return at most 10,000 rows.
:::

::: details Calling it from the app
```js
const tix = await login("/mcp/app:");
await tix.callTool("add-issue", { title: "Ferns need light" });
const open = await tix.callTool("list-issues", { status: "open" });
```

Over MCP, the same tools are at `/mcp/app:<nonce>` — so an agent can file
tickets for you.
:::
