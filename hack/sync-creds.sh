#!/usr/bin/env bash
# Materialize AWS credentials from a Pulumi ESC environment into the
# provider-credentials Secret consumed by runner Jobs. Only AWS_* variables
# are copied. Re-run whenever the (short-lived) credentials expire.
set -euo pipefail

NS=${NS:-pulumi-do-operator-system}
ESC_ENV=${ESC_ENV:-pulumi-idp/auth}
SECRET=${SECRET:-provider-credentials}

esc env open "$ESC_ENV" --format json | python3 -c '
import json, subprocess, sys

ns, secret = sys.argv[1], sys.argv[2]
env = json.load(sys.stdin).get("environmentVariables", {})
keys = sorted(k for k in env if k.startswith("AWS_"))
if not keys:
    sys.exit("no AWS_* variables found in the ESC environment")
args = [
    "kubectl", "create", "secret", "generic", secret, "-n", ns,
    "--dry-run=client", "-o", "yaml",
]
args += [f"--from-literal={k}={env[k]}" for k in keys]
manifest = subprocess.run(args, check=True, capture_output=True).stdout
subprocess.run(["kubectl", "apply", "-f", "-"], input=manifest, check=True)
print(f"synced {keys} into secret {ns}/{secret}", file=sys.stderr)
' "$NS" "$SECRET"
