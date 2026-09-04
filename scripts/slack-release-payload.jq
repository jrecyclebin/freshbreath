# Boil a GitHub release down to the one paragraph worth reading in Slack.
#
# Inputs (all --arg): name, body, commit, url.
# Output: the JSON object to POST to an incoming webhook.
#
# The shape we're after is a headline and a lead, which is what the release
# notes already open with — a `## Heading` and the paragraph under it. So we
# take those two and leave the rest behind the link; nobody reads a full
# release note in a channel, they read the first sentence and decide.

def normalize:      gsub("\r"; "");
def trim:           sub("^\\s+"; "") | sub("\\s+$"; "");
def is_heading:     test("^#{1,6}\\s");
def strip_heading:  sub("^#{1,6}\\s+"; "");

# Escape first, then build links: our own <url|text> markup has to be added
# after the escaper has stopped looking for angle brackets, or we'd escape
# the very thing we just wrote.
def slack_escape:   gsub("&"; "&amp;") | gsub("<"; "&lt;") | gsub(">"; "&gt;");
def md_links:       gsub("\\[(?<t>[^\\]]*)\\]\\((?<u>[^)\\s]+)\\)"; "<\(.u)|\(.t)>");
def md_bold:        gsub("\\*\\*"; "*");          # **strong** is *strong* here
def mrkdwn:         slack_escape | md_links | md_bold;

# Cut on a word boundary so we never hand Slack half a URL.
def clip($n):       if length > $n then (.[0:$n] | sub("\\s+\\S*$"; "")) + " …" else . end;

# A release published by `gh release create --generate-notes` has a body
# consisting solely of this line. That is not news, it's a footer.
def is_changelog:   test("^\\*\\*Full Changelog\\*\\*");

($body | normalize | split("\n\n") | map(trim) | map(select(length > 0))
       | map(select(is_changelog | not))) as $blocks

# Only a heading in first position is this release's headline. A `## Everything
# else` further down is a section of the note, not the name of the thing.
| (if ($blocks | length) > 0 and ($blocks[0] | is_heading)
   then ($blocks[0] | strip_heading) else "" end) as $heading

| ($blocks | map(select(is_heading | not)) | first // "") as $lead

# No prose in the body yet — notes get written after publishing — so fall
# back to what the tag actually points at. Degrades to the old behaviour
# rather than announcing a compare URL and calling it a release note.
| (if ($lead | length) > 0 then $lead else $commit end) as $lead

| (if ($heading | length) > 0
   then "*\($name | mrkdwn)* — \($heading | mrkdwn)"
   else "*Fresh Breath \($name | mrkdwn)* is out on Github" end) as $title

| { text: "\($title)\n\($lead | mrkdwn | clip(600))\n<\($url)|View the release>" }
