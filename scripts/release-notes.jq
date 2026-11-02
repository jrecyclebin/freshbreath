# Boil a GitHub release down to the one paragraph worth reading in a channel.
# Shared by slack-release-payload.jq and discord-release-payload.jq, which
# `include` it and only differ in how they dress the result up.
#
# The shape we're after is a headline and a lead, which is what the release
# notes already open with — a `## Heading` and the paragraph under it. So we
# take those two and leave the rest behind the link; nobody reads a full
# release note in a channel, they read the first sentence and decide.

def normalize:      gsub("\r"; "");
def trim:           sub("^\\s+"; "") | sub("\\s+$"; "");
def is_heading:     test("^#{1,6}\\s");
def strip_heading:  sub("^#{1,6}\\s+"; "");

# Cut on a word boundary so we never hand a channel half a URL.
def clip($n):       if length > $n then (.[0:$n] | sub("\\s+\\S*$"; "")) + " …" else . end;

# A release published by `gh release create --generate-notes` has a body
# consisting solely of this line. That is not news, it's a footer.
def is_changelog:   test("^\\*\\*Full Changelog\\*\\*");

# -> { heading, lead }, both raw markdown; heading may be "".
def release_parts($body; $commit):
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
  | { heading: $heading,
      lead: (if ($lead | length) > 0 then $lead else ($commit | trim) end) };
