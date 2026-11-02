# Dress a release up as a Slack incoming-webhook message.
#
# Inputs (all --arg): name, body, commit, url, docker_url.
# Output: the JSON object to POST to an incoming webhook.
# Run with `-L scripts` so the include below resolves.

include "release-notes";

# Escape first, then build links: our own <url|text> markup has to be added
# after the escaper has stopped looking for angle brackets, or we'd escape
# the very thing we just wrote.
def slack_escape:   gsub("&"; "&amp;") | gsub("<"; "&lt;") | gsub(">"; "&gt;");
def md_links:       gsub("\\[(?<t>[^\\]]*)\\]\\((?<u>[^)\\s]+)\\)"; "<\(.u)|\(.t)>");
def md_bold:        gsub("\\*\\*"; "*");          # **strong** is *strong* here
def mrkdwn:         slack_escape | md_links | md_bold;

release_parts($body; $commit) as {$heading, $lead}

| (if ($heading | length) > 0
   then "*\($name | mrkdwn)* — \($heading | mrkdwn)"
   else "*Fresh Breath \($name | mrkdwn)* is out on Github" end) as $title

# Container images are a separate, manual step: built from the very tarballs
# this release just attached, and only once a human has decided the release
# is a keeper (see .github/workflows/docker.yml). So the note ends with the
# handle for that door. Absent docker_url, the line simply isn't there.
| (if ($docker_url | length) > 0
   then "\n<\($docker_url)|Build the Docker images →>" else "" end) as $docker

| { text: "\($title)\n\($lead | mrkdwn | clip(600))\n<\($url)|View the release>\($docker)" }
