# AWS examples — 25 composites + a component

Twenty-five platform patterns for AWS, each a cluster-scoped
`DoCompositeDefinition` (the reusable graph) plus **two** `DoComposite`
instances — "dev" and "prod" — that prove the same definition instantiates
more than once without collision (every name carries a random suffix). Plus
one example driving a real Pulumi **component** through the component engine.

All resources are pinned to `aws@7.34.0` (the version baked into the runner
image) and chosen to be cheap/fast and to create *and* delete cleanly.

## Prerequisites

Operator deployed (see top-level README) and AWS credentials synced from the
Pulumi ESC environment `pulumi-idp/auth` into the `provider-credentials`
Secret. The example 26 component additionally needs `PULUMI_ACCESS_TOKEN`:

```sh
INCLUDE_PULUMI_TOKEN=1 ./hack/sync-creds.sh   # re-run when the OIDC creds expire (~1h)
```

The manager needs **1Gi** memory for AWS workloads (the default 256Mi
OOM-crashloops under many concurrent resource types — see the top-level
deploy manifests).

## The examples

| # | File | Pattern | Services |
|---|------|---------|----------|
| 01 | `01-secure-bucket.yaml` | Encrypted, versioned, TLS-only S3 bucket | S3 |
| 02 | `02-static-website.yaml` | Public static-website bucket (ordered behind the public-access-block) | S3 |
| 03 | `03-bucket-lifecycle-cors.yaml` | Versioning + lifecycle + CORS | S3 |
| 04 | `04-iam-app-role.yaml` | App role + standalone permission policy | IAM |
| 05 | `05-dynamodb-table.yaml` | Single-table design (GSI, TTL) + data-access policy | DynamoDB, IAM |
| 06 | `06-sns-sqs-fanout.yaml` | SNS → SQS fan-out with queue policy | SNS, SQS |
| 07 | `07-sqs-fifo-dlq.yaml` | FIFO queue + dead-letter redrive | SQS |
| 08 | `08-log-group-alarm.yaml` | Log group + metric alarm → SNS | CloudWatch, SNS |
| 09 | `09-ecr-repo.yaml` | ECR repo + lifecycle + repo/pull policies | ECR, IAM |
| 10 | `10-ssm-param-hierarchy.yaml` | SSM Parameter Store config tree | SSM |
| 11 | `11-secret-with-policy.yaml` | Secrets Manager secret + version + read policy | Secrets Manager, IAM |
| 12 | `12-kms-key-alias.yaml` | KMS key + alias + usage policy | KMS, IAM |
| 13 | `13-private-dns-zone.yaml` | Route 53 private zone (VPC) + A/CNAME records | Route 53, VPC |
| 14 | `14-vpc-network.yaml` | VPC + IGW + subnet + route table + route | VPC/EC2 |
| 15 | `15-bucket-notify-sns.yaml` | S3 → SNS object-created notifications | S3, SNS |
| 16 | `16-composite-alarm.yaml` | Composite alarm over warn + crit metric alarms | CloudWatch |
| 17 | `17-eventbridge-to-sns.yaml` | Scheduled EventBridge rule → SNS | EventBridge, SNS |
| 18 | `18-log-metric-filter.yaml` | Log metric filter + alarm on the derived metric | CloudWatch |
| 19 | `19-security-group.yaml` | VPC security group + standalone ingress/egress rules | VPC/EC2 |
| 20 | `20-step-functions.yaml` | Step Functions state machine + execution role | Step Functions, IAM |
| 21 | `21-vpc-s3-endpoint.yaml` | VPC + route table + S3 gateway endpoint | VPC/EC2 |
| 22 | `22-appconfig.yaml` | AppConfig application + environment + profile | AppConfig |
| 23 | `23-cloudwatch-dashboard.yaml` | Log group + alarm + dashboard | CloudWatch |
| 24 | `24-sns-sqs-encrypted.yaml` | KMS-encrypted SNS → SQS | KMS, SNS, SQS |
| 25 | `25-sqs-consumer-role.yaml` | Queue + consumer role + access policy | SQS, IAM |
| 26 | `26-component-secure-bucket.yaml` | A Pulumi **component** (`aws-platform:SecureBucket`) via the component engine | S3 |

## Walkthrough

```sh
# one example, two stacks
kubectl apply -f examples/aws/01-secure-bucket.yaml
kubectl get docomposites -w                 # bkt-dev, bkt-prod → 6/6 READY
kubectl get doresources | grep bkt-         # every rendered child is its own object

# update flows through the composition
kubectl patch docomposite bkt-dev --type merge -p '{"spec":{"parameters":{"env":"staging"}}}'

# teardown garbage-collects children in reverse dependency order
kubectl delete -f examples/aws/01-secure-bucket.yaml
kubectl get doresources | grep bkt-         # drains to empty; buckets deleted in AWS
```

## Design notes

- **Two stacks per definition.** Each file ends with two `DoComposite`
  objects (dev/prod). A random-suffix child feeds every account/region/global
  unique name, so both stacks — and repeat runs — never collide.
- **One sibling source per value.** A composite property value may interpolate
  only one distinct `${resources.*}` expression (`${params.*}`/`${self.*}`
  inline freely). Policies needing two sibling ARNs are reduced to one source;
  where ordering matters, a resource references an upstream *policy's* output
  (which echoes the ARN) to both get the value and gate creation order.
- **Stateless delete (pulumi >= the #23837 fix).** `pulumi do delete` currently
  receives only the resource ID — no input state — so any resource whose
  provider `Delete` reads input properties cannot be torn down on the pinned
  runner `pulumi` 3.250.0. This is broader than IAM: it includes
  `aws:ssm/parameter`, `aws:ecr/repositoryPolicy`, `aws:route53/record`,
  `aws:ec2/route`, `aws:cloudwatch/logMetricFilter`,
  `aws:cloudwatch/eventTarget`, `aws:vpc/securityGroup{Ingress,Egress}Rule`,
  `aws:appconfig/environment`, `aws:iam/rolePolicyAttachment`,
  `aws:iam/instanceProfile` and roles with `managedPolicyArns`. Resources whose
  delete needs only the ID (S3, DynamoDB, SNS/SQS, KMS, VPC, VPC endpoints,
  security groups, Step Functions, CloudWatch alarms/dashboards, standalone
  `iam/role` and `iam/policy`, …) tear down cleanly.

  **Verified on the pinned runner (pulumi 3.250.0):** all 26 create cleanly
  (both stacks). Full create **and** delete verified for **01–08, 11, 12, 15,
  16, 20, 21, 23, 24, 25, 26** (18). Create-verified with delete pending the
  upstream fix (pulumi/pulumi#23837) for **09, 10, 13, 14, 17, 18, 19, 22** (8)
  — they contain one of the input-state-on-delete resources above. The
  component (26) runs the **stateful** engine and has no such limitation.
- **Cost.** Everything here is free or a few cents (S3, IAM, DynamoDB
  on-demand, SNS/SQS, CloudWatch, ECR, SSM, VPC without NAT, AppConfig, Step
  Functions). KMS keys enter a 7-day `PendingDeletion` on teardown; Secrets
  Manager secrets delete immediately (`recoveryWindowInDays: 0`).
