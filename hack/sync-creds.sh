#!/usr/bin/env bash
# Materialize credentials into the provider-credentials Secret consumed by
# runner Jobs. Re-run whenever short-lived credentials expire.
#
#   AWS_* variables        from the ESC environment $ESC_ENV (default on)
#   PULUMI_ACCESS_TOKEN    from the local pulumi login when
#                          INCLUDE_PULUMI_TOKEN=1 (needed to resolve packages
#                          from the Pulumi Cloud private registry)
#   KUBECONFIG_CONTENT     an in-cluster-reachable kubeconfig for the kind
#                          cluster $KIND_CLUSTER when INCLUDE_KIND_KUBECONFIG=1
#                          (needed by components that deploy to Kubernetes;
#                          demo-grade: grants runner pods admin on the cluster)
set -euo pipefail

NS=${NS:-doplane-system}
ESC_ENV=${ESC_ENV:-pulumi-idp/auth}
SECRET=${SECRET:-provider-credentials}
INCLUDE_PULUMI_TOKEN=${INCLUDE_PULUMI_TOKEN:-0}
INCLUDE_KIND_KUBECONFIG=${INCLUDE_KIND_KUBECONFIG:-0}
KIND_CLUSTER=${KIND_CLUSTER:-doplane}

EXTRA_JSON="{}"
if [ "$INCLUDE_PULUMI_TOKEN" = "1" ]; then
  TOKEN=$(python3 -c "import json;print(json.load(open('$HOME/.pulumi/credentials.json'))['accessTokens']['https://api.pulumi.com'])")
  EXTRA_JSON=$(python3 -c "import json,sys; d=json.loads(sys.argv[1]); d['PULUMI_ACCESS_TOKEN']=sys.argv[2]; print(json.dumps(d))" "$EXTRA_JSON" "$TOKEN")
fi
if [ "$INCLUDE_KIND_KUBECONFIG" = "1" ]; then
  KC=$(kind get kubeconfig --name "$KIND_CLUSTER" | sed 's|server: https://127.0.0.1:[0-9]*|server: https://kubernetes.default.svc|')
  EXTRA_JSON=$(python3 -c "import json,sys; d=json.loads(sys.argv[1]); d['KUBECONFIG_CONTENT']=sys.argv[2]; print(json.dumps(d))" "$EXTRA_JSON" "$KC")
fi

esc env open "$ESC_ENV" --format json | python3 -c '
import json, subprocess, sys

ns, secret, extra = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
env = json.load(sys.stdin).get("environmentVariables", {})
values = {k: env[k] for k in sorted(env) if k.startswith("AWS_")}
values.update(extra)
if not values:
    sys.exit("no credentials to sync")
args = [
    "kubectl", "create", "secret", "generic", secret, "-n", ns,
    "--dry-run=client", "-o", "yaml",
]
args += [f"--from-literal={k}={v}" for k, v in values.items()]
manifest = subprocess.run(args, check=True, capture_output=True).stdout
subprocess.run(["kubectl", "apply", "-f", "-"], input=manifest, check=True)
print(f"synced {sorted(values)} into secret {ns}/{secret}", file=sys.stderr)
' "$NS" "$SECRET" "$EXTRA_JSON"
