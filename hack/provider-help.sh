#!/usr/bin/env bash
# provider-help.sh — generated help for a provider resource type.
#
# Reads the provider schema (pulumi package get-schema) and prints, for one
# resource token: required inputs, optional inputs, output paths usable in
# spec.references, and a ready-to-edit DoResource example.
#
#   ./hack/provider-help.sh random@4.21.0 random:index/randomPet:RandomPet
#   ./hack/provider-help.sh aws@7.34.0 aws:s3/bucketV2:BucketV2
#
# Requires: pulumi, jq.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <package>[@version] <resource-token>" >&2
  echo "example: $0 random@4.21.0 random:index/randomPet:RandomPet" >&2
  exit 2
fi

pkg="$1"
token="$2"

schema=$(pulumi package get-schema "$pkg")
resource=$(jq -e --arg t "$token" '.resources[$t]' <<<"$schema" 2>/dev/null) || {
  echo "resource token '$token' not found in schema of $pkg" >&2
  echo "similar tokens:" >&2
  jq -r '.resources | keys[]' <<<"$schema" | grep -i -- "${token##*:}" | head -10 >&2 || true
  exit 1
}

name=$(jq -r '.name' <<<"$schema")
version=$(jq -r '.version' <<<"$schema")

# describeInput renders "- key (type): first line of description".
describe='
  def firstline: if . then ": " + (split("\n")[0]) else "" end;
  def typ: (.type // .["$ref"] // "object") | tostring;
  "- " + $k + " (" + ($p | typ) + ")" + ($p.description | firstline)
'

echo "# ${token} (${name}@${version})"
echo
echo "## Required inputs"
required=$(jq -r --argjson r "$resource" '
  ($r.requiredInputs // [])[] as $k | $r.inputProperties[$k] as $p | '"$describe"'' <<<"{}")
echo "${required:-- (none)}"
echo
echo "## Optional inputs"
jq -r --argjson r "$resource" '
  ($r.requiredInputs // []) as $req
  | ($r.inputProperties // {}) | to_entries[]
  | select(.key as $k | $req | index($k) | not)
  | .key as $k | .value as $p | '"$describe"'' <<<"{}"
echo
echo "## Outputs (usable as spec.references fieldPath: status.outputs.<name>)"
jq -r '.properties // {} | keys[] | "- status.outputs.\(.)"' <<<"$resource"
echo
echo "## Example"
cat <<YAML
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: my-$(tr ':/' '--' <<<"${token##*:}" | tr '[:upper:]' '[:lower:]')
spec:
  type: ${token}
  package: ${name}@${version}
  properties:
$(jq -r '(.requiredInputs // [])[] as $k | "    \($k): # \(.inputProperties[$k].type // "see schema")"' <<<"$resource")
YAML
echo
echo "# NOTE: check provider docs for billable resources before applying."
