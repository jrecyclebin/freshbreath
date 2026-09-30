# Writing Tool Scripts for Tasks

Task services offer a way to write a set of custom tools that are each assigned
shell scripts (in bash or Powershell, depending on the OS) to allow Fresh Breath
apps to pass data to and from the underlying system.

Input to each of these tools happens through environment variables. Output from
these scripts happens on stdout. (Or stderr in the case of errors.)

The tool scripts for a task service are contained in a single text file which
can be uploaded using the `write_service_file` MCP tool. A sample tool script
looks like this:

```
[tool-name] Tool description here.

echo "$TASK" >> task.log # Log the name of the task being executed
---
[import] For importing files from the browser.

mv $TASK_MEME_IMAGE tmp/ # An argument named 'meme_image' is required for this
                         # task - a file path is supplied as the var.
printf '{"task":"%s","notes":"%s"}' "$TASK" "$TASK_NOTES" # The name of the task
                        # and the contents of the 'notes' argument are printed to stdout as a JSON object.
```

Some important points to note about the tool scripts:

- Each tool script is separated by a line containing three dashes (`---`).
- The first line of each tool script contains the name of the tool and a brief
  description.
- The tool scripts can use any environment variables - including those set by
  the task service itself.
- Files uploaded to the task service are (by default) pathed according to the
  uploaded file's name. For example, if a file named `image.png` is uploaded,
  its path will be something like '/tmp/image.png'. This way you can have the
  original name and extension in case you need it.

## Calling Conventions

Generally, the task service is called by using `callTool` from the ServiceProxy
object. The first argument is the name of the tool to call, and the second
argument is a dictionary of arguments to pass to the tool.

So, to call `import` above:

```js
const meme_image = document.getElementById('img_upload').files[0];
const notes = document.getElementById('notes').value.trim();
const obj = await taskService.callTool('import', { meme_image, notes });
console.log('Tool result:', obj);
```

As depicted, most arguments should be either strings or files. The `callTool`
method will automatically handle injecting these as environment variables. If
you need to pass in more complex data, such as arrays or objects, then `callTool`
will serialize those to JSON and you can use appropriate tools (such as `jq` in
bash) to parse them.

The example also illustrates receiving JSON back from a script. The `callTool`
method will automatically parse the JSON output and return it as an object. If
the script does not return valid JSON, then `callTool` will just return the raw
string output. A script that exits non-zero makes `callTool` throw, with the
script's output and stderr in the error message.

Tools can also be listed using the `listTools` method of the task service. This
will return an array of objects, each containing the name and description of a
tool.

## As an MCP

A task service is also an MCP server, mounted at `/mcp/{name}` like a virtual
service and behind the same gate. Point Claude Desktop (or any MCP client) at
it and each task is a tool.

Scripts don't declare their arguments, so the tool's argument list is read off
the script: every `$TASK_NAME`, `${TASK_NAME}` or `$env:TASK_NAME` it mentions
becomes a `name` argument, typed as a string and optional. (`$TASK_TOKEN` is
left out — the server fills that in.) Mention every argument you expect in the
script itself, even ones you only pass along, or an MCP client won't know to
send it.

The result is the script's stdout, and a non-zero exit is a tool error
carrying stderr.

There's no file upload over MCP: tool arguments are plain JSON, with no way
to attach a file. An argument that's a file when an app calls the task is just
a string when an MCP client does — a path on the server, say, or whatever the
client sends.
