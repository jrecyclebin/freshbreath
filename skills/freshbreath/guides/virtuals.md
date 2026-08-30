# Writing Tool Scripts for Virtual Services

Virtual services are simply wrappers around API calls (or any kind of HTTP
request) into a set of tools that can be used as an MCP or from the ServiceProxy
`callTool` function. These tool scripts are a custom text format that describes
a series of HTTP requests, followed by a response: usually a JSON object. They
can also wrap SQL queries against a database — see the SQL Steps section
below.

Let's dig into a quick example for a Github tool script:

```
[create-pull-request] Open a new pull request in a repository.

POST https://api.github.com/repos/$owner/$repo/pulls
Authorization: Bearer $token
Content-Type: application/json
{
  "title": $title,
  "body": $body,
  "head": $head,
  "base": $base
}

HTTP 201
---
[list-pull-requests] List pull requests for a repository, optionally filtered by state.
# $state is one of: open, closed, all (default: open)

GET https://api.github.com/repos/$owner/$repo/pulls?state=$state&per_page=50
Authorization: Bearer $token

HTTP 200
```

This script describes two tools: `create-pull-request` and `list-pull-requests`.
Each tool is separated by a line containing three dashes (`---`). The first line
of each tool script contains the name of the tool and a brief description. The
rest of the tool script is a series of HTTP requests, followed by the expected
response.

A virtual service tool script has this basic structure:

- Tool header: `[tool-name] Tool description here.`
- HTTP request: The HTTP method, URL, headers, and body (if applicable).
- (Optional) Additional requests: each starting with the HTTP method and URL,
  followed by headers and body.
- Expected response: The expected HTTP status code and any response body.

If no response body is specified, the final request's response body will be
returned as-is. So, in the `list-pull-requests` tool above, the response body
from Github will be returned as-is.

Aside from the request and response layouts, tool scripts can also include:

- Comments: Lines starting with `#` are comments and are ignored by the parser.
- Type annotations: Lines starting with `$arg is type` indicate that the argument
  is expected to be of a certain type (string, object, number, boolean, array)
  and whether it is optional with a question mark (`number?`).
- Assignments: Lines starting with `$var = expression` assign a value to a
  variable. The expression can be a string, number, boolean, object, or array.
- Assertions: Lines starting with `assert` can be used to assert that a variable is of
  a certain type or has a certain value. If the assertion fails, the tool will
  return a custom error with a 400 response.

These additions affect the HTTP requests that they precede, and can be used to
modify the request or response.

## Calling Convention

Generally speaking, arguments to the tools for a virtual service are inferred
from the `$`-prefixed variables throughout the tool script. It is assumed that
these variables will be strings - and all are required arguments. Fresh Breath
simply takes stocks of all variables used and expects them to be passed in as
arguments to the tool.

The Fresh Breath `callTool` method (available for ServiceProxy objects) can be
used to call a tool in a virtual service. The first argument is the name of the
tool to call, and the second argument is a dictionary of arguments to pass to
the tool.

So, to call `list-pull-requests` above:

```
const prs = await githubService.callTool('list-pull-requests', {
  owner: 'octocat',
  repo: 'Hello-World',
  state: 'open'
});
```

The `$token` variable is a special case: Fresh Breath fills it in with the
credential that should go upstream, and *which* credential that is depends on
the service's **acts as** slot:

* **A stored record** (api_key, ssh_key) — that credential, the same for every
  caller. This is the one that works with nobody present.
* **An interactive record** (oidc, oauth2) — the caller's own credential for
  that provider, unsealed from their Fresh Breath token. The caller has to have
  logged in to that record.
* **Empty** — the caller's own credential, whatever their gate yielded. Behind
  an open gate the caller's `Authorization` simply rides through untouched;
  behind a passphrase gate there is no upstream credential at all, so `$token`
  is empty.

(Technically the browser holds a Fresh Breath token with upstream credentials
sealed inside it, unreadable there; the server unseals the right one on the way
out. The same resolution feeds the HTTP proxy, so the two can't drift.)

**Unattended callers.** A Scheduled Task, or anything else running with nobody
in front of it, has no interactive credential to offer. If a tool needs to reach
upstream unattended, its service must **act as a stored record** — an api_key or
ssh_key. An interactive record fails the call rather than silently sending
nothing, and the error says which record needs the login.

A few other variables that come with token:

* `$token_email`: the user's email address.
* `$token_id`: the user's numeric ID in Fresh Breath.
* `$token_sub`: the user's 'sub' claim.

If a tool uses any of these, the caller must be logged in - the call fails
otherwise. (`$token_id` is the one exception in spirit: a logged-in user
without a Fresh Breath account gets `null` - see the SQL section below.)

There are some additional ways to format JSON responses in tool scripts, which
can be seen in the example below for a SharePoint virtual service:

```
[get-site] Resolve a SharePoint site URL to a site ID.

$hostname = host($url)
$pathname = path($url)
GET https://graph.microsoft.com/v1.0/sites/$hostname:$pathname
Authorization: Bearer $token

HTTP 200
---
[add-list-item] Add a new item to a list.
# Adjust the fields object to match your list's actual columns.

$fields is object
POST https://graph.microsoft.com/v1.0/sites/$site_id/lists/$list_id/items
Authorization: Bearer $token
Content-Type: application/json
{
  "fields": $fields
}

HTTP 201
---
[get-list-next] Fetch the next page of items using the @odata.nextLink from a previous response.

assert(host($next_link) == "graph.microsoft.com", "Invalid nextLink")
GET $next_link
Authorization: Bearer $token

HTTP 200
---
[update-list-item] Update fields on an existing item.
# Adjust the body to match the columns you want to update.

PATCH https://graph.microsoft.com/v1.0/sites/$site_id/lists/$list_id/items/$item_id/fields
Authorization: Bearer $token
Content-Type: application/json
{...$fields}

HTTP 200
```

First off, notice the use of the `host` and `path` functions. These can be used
in expressions to extract the hostname and path from a URL stored in a variable.
(Note that the `host` function is also used in the expression for the `assert`
statement in the `get-list-next` tool.) Those custom variables can also be used
in the JSON response - like arguments can.

Variables that are *assigned* in the tool script are not treated as arguments - so
those names will be eliminated from the tool's argument list.

We also have two examples of object output in the response JSON:

- The `add-list-item` tool embeds the `fields` object in the request body.
- The `update-list-item` tool uses the spread operator to merge the `fields`
  object into the request body.

The `add-list-item` tool above demonstrates the use of a type annotation, to
indicate that the `fields` argument is expected to be an object - and it will
be typed that way in the tool definition in the Fresh Breath MCP endpoint for
this service.

Possible types for tool arguments are: 'string', 'object', 'number', 'boolean',
and 'array'. The default type is 'string' if no type is specified.

If multiple variables share a type, they can also share a definition:

```
$offset, $count is number?
```

This declares two optional numerical arguments for the tool.

## Object Traversal

In the case of object arguments and responses, the tool script can use dot
notation in expressions - to traverse the object and access nested properties.

For example, if a response returns a JSON object like this:

```
{
  "data": {
    "items": [
      { "id": 1, "name": "Item 1" },
      { "id": 2, "name": "Item 2" }
    ]
  }
}
```

The tool script can access the `items` array using `$.data.items`.
Alternatively, the bracket notation can be used: `$['data']['items']`. Both
notations are equivalent. (Both of these syntaxes are supported thanks to the
`gson` library used in Fresh Breath.)

## String Bodies

To send raw file data or string content in place of a JSON object, use the
string spread operator without the curly braces.

```
[upload-file] Upload a file's contents to a path.

PUT https://api.example.com/files/$path
Authorization: Bearer $token
Content-Type: text/plain
...$content

HTTP 200
```

Be sure to set a `Content-Type` header. String-spread bodies don't pick a
default for you and missing it is an error.

Since the spread takes an expression, you can use the `base64dec` and
`base64enc` functions here as well, if needed:

```
...base64dec($content)
```

Here `$content` is the base64 text of the PNG; `base64dec($content)` decodes it
and the raw PNG bytes are what hits the wire. (`base64enc` is the inverse — it
encodes a string to base64 text.)

## SQL Steps — Querying a Database

A tool script step doesn't have to be an HTTP request. A step's verb can be a
SQL keyword instead of an HTTP method, and the tool becomes a query against a
SQLite database.

By default, these SQL tools point to an app's built-in `app.db` database.
In the service settings you can configure a global database - or a specific
app's database - to be used. (Can be useful for when you want multiple apps
to work out of the same database.)

```
[list-tasks] List tasks, newest first.

SELECT id, title, done
  FROM tasks
  WHERE done = $done
  ORDER BY created_at DESC
  LIMIT 20
---
[add-task] Add a task to the list.

INSERT INTO tasks (title, done)
  VALUES ($title, 0)
---
[migrate] Create the necessary tables if they don't exist.

CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  done INT DEFAULT 0,
  created_at TEXT DEFAULT (datetime('now')))
```

Recognized verbs: `SELECT`, `INSERT`, `UPDATE`, `DELETE FROM`, `REPLACE`,
`WITH`, `CREATE`, `DROP`, `ALTER` (case-insensitive). A SQL step is the verb
line plus any **indented** continuation lines beneath it — it ends at the
first non-indented line, so indent your `WHERE` clauses and format the query
however reads best. Blank lines and indented `#` comments inside a step are
skipped. (And ensure that all closing parentheses end up indented as well.)

The `DELETE FROM` spelling is load-bearing: SQL's DELETE is always followed
by `FROM`, and that's how the parser tells it apart from an HTTP
`DELETE https://…` request. Both kinds of step can live in one file.

There's no `HTTP 200`-style status line on a SQL step. SQL has no status code
— a failing statement fails the tool, and the absence of the line is the
signal.

### Variables are bindings, not splices

`$title` in a URL template is a string splice. `$title` in a SQL step is a
**bound parameter** — compiled to a named SQLite binding at load time, so
there is no injection surface. Pretty nice - and the syntax is familiar from
the HTTP calls.

Two rules keep this honest:

- **No `$var` inside single-quoted string literals.** `WHERE name LIKE
  '%$term%'` can't be a binding, so it's a load-time error with a suggestion.
  Write `WHERE name LIKE $pattern` and let the caller pass `%frog%` — wildcards
  and all.
- **`$$` escapes a literal `$`** for the rare query that needs one.

For auth, `$token` works here too, bound like any other variable. But it's
much more useful to use `$token_id` - the user's Fresh Breath ID - to assign
data to them in 'user_id' columns in your database. If you need a string
version, use `$token_email` or `$token_sub` instead. Especially if you are
storing heterogenous users from different systems. (Like if you need to store
data for users that don't have Fresh Breath accounts!)

As for `?`-annotated optional parameters, any omitted bind as `NULL`,
while an explicit empty string stays `""` — the difference between "not
given" and "given as nothing" survives the trip to the database.

### Working with the result

A SQL step's result is the same object the database API returns:

```
{
  "columns": ["id", "title", "done"],
  "rows": [[1, "Feed the frogs", 0]],
  "rowsAffected": 0,
  "lastInsertId": 0,
  "truncated": false
}
```

So `$.rows`, assignments, and `{...}` shaping blocks all work against it,
exactly as they do after an HTTP response:

```
[recent-tasks] The ten most recent open tasks.

SELECT id, title
  FROM tasks
  WHERE done = 0
  ORDER BY created_at DESC
  LIMIT 10

{
  "tasks": $.rows
}
```

And because steps are steps, HTTP and SQL mix freely in one tool — fetch from
anywhere, store locally:

```
[import-issue] Fetch a GitHub issue and file it locally.

GET https://api.github.com/repos/$owner/$repo/issues/$number
Authorization: Bearer $token

HTTP 200

$title = $.title
$body = $.body

INSERT INTO issues (title, body, source)
  VALUES ($title, $body, 'github')
```

Great for adding logging and caching to your API call tools.

### Which database does a service target?

The database isn't declared in the tool script. It's service configuration,
set in the control panel when creating the virtual service:

| Database target | Meaning |
|---|---|
| *(unset)* | Each linked app gets its own database. The common case. |
| `global` | A database shared across apps (name it in the database name field). |
| `app:<nonce>` | Pinned to one specific app's database. |

With the default (unset) target, one set of queries links to many apps and
each app reads and writes its own file — reuse without sharing. The database
name defaults to `app`, and databases are created on first touch, so there's
no registration step: your `CREATE TABLE IF NOT EXISTS` tool is the migration.

Two access facts worth knowing in plain words:

- **Over MCP, default-target SQL tools gain a required `app_nonce` argument**
  — the server adds it to the tool schema, because an MCP client has no
  ambient app. (The name `$app_nonce` is therefore reserved in your scripts.)
  Browser-side calls supply the app automatically.
- **On the browser path, the app↔service link is the whole grant.** A public
  page can run every SQL tool linked to its app — which is how a public
  guestbook is supposed to work, but it means a `global`-target service
  linked to an unsecured app hands its visitors the shared database. Link
  global services sparingly.

The engine behind these steps is hardened SQLite: ATTACH and extension
loading are refused, PRAGMA is allowlisted to read-only schema inspection,
statements run under a 5-second deadline, and results cap at 10,000 rows —
reported honestly as `"truncated": true` rather than silently shortened.

> ⚡ **YOUR TABLES BECOME MCP TOOLS**
>
> Virtual services are already mounted at `/mcp/{name}`, so a parameterized
> SQL step is an MCP tool with no additional work — named, typed, bound. The
> caller varies the bindings, never the query, so a model calling
> `recent-tasks` can't be talked into `DROP TABLE` no matter how confidently
> it asks.
