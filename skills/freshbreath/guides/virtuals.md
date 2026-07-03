# Writing Tool Scripts for Virtual Services

Virtual services are simply wrappers around API calls (or any kind of HTTP
request) into a set of tools that can be used as an MCP or from the ServiceProxy
`callTool` function. These tool scripts are a custom text format that describes
a series of HTTP requests, followed by a response: usually a JSON object.

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
  is expected to be of a certain type (string, object, number, boolean, array).
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

The `$token` variable is a special case: it is passed in automatically by
Fresh Breath to the script using the user's creds for the upstream service.
(Technically Fresh Breath delivers the user a Fresh Breath token to access the
virtual service, and the virtual service unwraps that token to get the
upstream creds - those creds are passed in as the `$token` variable.)

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

Variables that are assigned in the tool script are not treated as arguments - so
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
