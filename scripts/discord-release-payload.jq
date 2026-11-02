# Dress a release up as a Discord webhook message.
#
# Inputs (all --arg): name, body, commit, url, docker_url.
# Output: the JSON object to POST to a Discord webhook.
# Run with `-L scripts` so the include below resolves.
#
# Discord already speaks markdown, so unlike Slack there's no translating
# links or bold — the release note's own [text](url) and **strong** go
# through as written.

include "release-notes";

release_parts($body; $commit) as {$heading, $lead}

| (if ($heading | length) > 0
   then "**\($name)** — \($heading)"
   else "**Fresh Breath \($name)** is out on Github" end) as $title

# Same manual-Docker door as the Slack note. The <angle brackets> stop
# Discord unfurling the Actions page into a big useless preview card.
| (if ($docker_url | length) > 0
   then "\nDocker images updated at [ghcr.io](https://github.com/jrecyclebin/freshbreath/pkgs/container/freshbreath) and [Docker Hub](https://hub.docker.com/r/jrecyclebin/freshbreath)" else "" end) as $docker

# allowed_mentions with an empty parse list: a release note that happens to
# say @everyone (or <@&role>) is text, not a page to the whole server.
| { content: "\($title)\n\($lead | clip(600))\n[View the release](\($url))\($docker)",
    allowed_mentions: { parse: [] } }
