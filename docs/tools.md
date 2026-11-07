# Tools and Databases

<div class="short">

A tool file is plain text: a `[name] description` line, then the HTTP request or SQL query the tool runs, with `$words` for the blanks. Tools are separated by `---`. Every tool you write is callable from your app with `callTool()` *and* from an agent over MCP.

</div>

<div class="like" data-icon="📊">

**Think of a set of spreadsheets with labeled buttons.** The database is the spreadsheets. Each tool is a button that does one fixed thing — "add a row," "show open items." Callers fill in the blanks but can't change what the button does.

</div>

## What does this tool file make?

Edit the file on the left. The right side shows the tools it makes, their arguments, and the slips the server would refuse. Try *Something's off* to see the common ones.

<ToolPlayground />

## The rules your agent keeps tripping on

<div class="steps">

1. **Indent every SQL line after the first.** A SQL step ends at the first line that isn't indented, closing parenthesis included. An unindented `WHERE` cuts the query short.
2. **No `$blank` inside quotes.** `LIKE '%$term%'` can't be a binding. Write `LIKE $pattern` and let the caller pass `%frog%`.
3. **Delete is spelled `DELETE FROM`.** That's how the parser tells it apart from an HTTP `DELETE https://…`.
4. **Every `$blank` is a required string** unless you say otherwise: `$limit is number?`, `$state is "open" | "closed"`.
5. **`$app_nonce` is reserved.** The server adds it to database tools called over MCP.

</div>

## Blanks the server fills in

| Write | Get | Note |
|---|---|---|
| `$token` | The credential that goes upstream, per the service's [acts as](/auth#acts-as-what-goes-upstream) | Never an argument |
| `$token_id` | The caller's Fresh Breath user id | `null` for someone without an account |
| `$token_email` | The caller's email | The caller must be signed in |
| `$token_sub` | The caller's `sub` claim | Good for people from outside |

`$token_id` is the one you want for "who added this." Use `$token_email` or `$token_sub` when outsiders save data too.

::: tip Bindings, not splices
In a SQL step every `$blank` is a bound parameter, compiled in when the file loads. There's no way to inject SQL through one — an agent calling `list-tasks` can't be talked into `DROP TABLE`, however confidently it asks.
:::

## Which database?

| Service's database target | Means |
|---|---|
| *(unset)* | Each linked app gets its own database. The usual case. |
| `global` | One database shared across apps |
| `app:<nonce>` | Pinned to one app's database |

Databases are made the first time a tool touches them, so your `CREATE TABLE IF NOT EXISTS` tool *is* the migration. An **app service** always uses its own app's database, and only that app's pages can call it.

::: warning Linking is the grant
On the browser path, linking a service to an app lets that app's visitors run every tool in it. A `global` service linked to an open app hands its visitors the shared database. Link global services sparingly.
:::

## HTTP tools

The same file can wrap web APIs. A request line, headers, an optional body, and the status you expect:

```
[create-issue] Open an issue in a repository.

POST https://api.github.com/repos/$owner/$repo/issues
Authorization: Bearer $token
Content-Type: application/json
{ "title": $title, "body": $body }

HTTP 201
```

Values spliced into a URL path are escaped as one segment, so a blank can't walk out of its spot. `$x = <jq>` assigns from the last response, `assert(…; "message")` checks a condition, and a final `{ … }` block reshapes the result. A path like `GET /api/me` calls Fresh Breath itself.

## Limits

<div class="facts">

- ⏱️ **5 seconds** per statement
- 📏 **10,000 rows** at most, reported as `"truncated": true`
- 🧱 **No ATTACH or extensions** and only a few read-only PRAGMAs
- 🔎 **FTS5** full-text search is built in — ask your agent for a search box

</div>

The format has more — encrypted parameters, forms and links that ask the user something mid-tool. It's all in the `virtuals` guide:

```say
Read the Fresh Breath virtuals guide, then [what you want the tool to do].
```
