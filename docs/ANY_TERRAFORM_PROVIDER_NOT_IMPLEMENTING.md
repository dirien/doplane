# Any Terraform Provider: Not Implementing Now

Decision: do not implement first-class Any Terraform Provider support in
`doplane` yet.

Reason: live tests showed that `pulumi do --stateless delete` returned success
for parameterized Terraform providers while leaving the remote resource in
place. That is not safe for a Kubernetes operator because the controller could
remove a finalizer while the external resource still exists.

Keep this document as investigation evidence and as a checklist for revisiting
the feature later. It is not part of the current implementation plan.

## Sources

- Pulumi Any Terraform Provider docs:
  https://www.pulumi.com/docs/iac/concepts/providers/any-terraform-provider/
- Pulumi Any Terraform Provider registry page:
  https://www.pulumi.com/registry/packages/terraform-provider/
- Terraform Vercel provider:
  https://registry.terraform.io/providers/vercel/vercel/latest

## What Worked

Any Terraform Provider is usable by `pulumi do` for schema discovery and
create operations.

`pulumi do` accepts a parameterized package string:

```sh
pulumi do vercel:index/project:Project \
  --package "terraform-provider@1.1.4 vercel/vercel 5.3.0" \
  create --stateless --input-file input.pp --yes
```

`pulumi package get-schema` requires the base provider and parameters to be
split:

```sh
pulumi package get-schema terraform-provider@1.1.4 -- vercel/vercel 5.3.0
```

That means CRUD can pass one `--package` value, but schema fetch would need to
parse the package reference.

## Vercel Test

Test environment:

- Pulumi CLI: `v3.250.0`
- Base Pulumi provider: `terraform-provider@1.1.4`
- Terraform provider: `vercel/vercel@5.3.0`
- Token source: `VERCEL_API_TOKEN` from ESC environment `pulumi-idp/auth`

Schema result:

```json
{
  "name": "vercel",
  "version": "5.3.0",
  "parameterization": {
    "baseProvider": {
      "name": "terraform-provider",
      "version": "1.1.4"
    }
  },
  "resourceCount": 43
}
```

Generated Pulumi resource tokens included:

```text
vercel:index/project:Project
vercel:index/deployment:Deployment
vercel:index/projectDomain:ProjectDomain
vercel:index/projectEnvironmentVariable:ProjectEnvironmentVariable
```

Create worked. The tested project was created and returned a Vercel project id
such as `prj_kBhT6APrdggC2trVxuCoY5g4GqxQ`.

Delete did not work correctly. `pulumi do delete` exited successfully and
printed the delete intent, but the project still existed in Vercel:

```text
This will delete vercel:index/project:Project "prj_kBhT6APrdggC2trVxuCoY5g4GqxQ".
project remained after pulumi delete id=prj_kBhT6APrdggC2trVxuCoY5g4GqxQ
```

The test project was cleaned up with the Vercel REST API.

## DigitalOcean Control Test

The same delete behavior was tested with a cheap DigitalOcean `Tag` resource.
The token came from ESC path `pulumiConfig.digitalocean:token` in
`pulumi-idp/auth` and was passed as `DIGITALOCEAN_TOKEN`.

Any Terraform Provider:

```sh
pulumi do digitalocean:index/tag:Tag \
  --package "terraform-provider@1.1.4 digitalocean/digitalocean 2.94.0" \
  create --stateless --yes --input-file input.pp

pulumi do digitalocean:index/tag:Tag \
  --package "terraform-provider@1.1.4 digitalocean/digitalocean 2.94.0" \
  delete --yes <tag-name> --stateless
```

Observed result:

```text
dry-run ok tag=doplane-anyprovider-tag-1783438881
created DigitalOcean tag=doplane-anyprovider-tag-1783438881
api confirmed tag=doplane-anyprovider-tag-1783438881 total_resources=0
pulumi delete output:
This will delete digitalocean:index/tag:Tag "doplane-anyprovider-tag-1783438881".
tag remained after pulumi delete: doplane-anyprovider-tag-1783438881
api cleanup exit=0 tag=doplane-anyprovider-tag-1783438881
```

Native Pulumi DigitalOcean provider, same resource:

```sh
pulumi do digitalocean:index/tag:Tag \
  --package "digitalocean@4.73.0" \
  create --stateless --yes --input-file input.pp

pulumi do digitalocean:index/tag:Tag \
  --package "digitalocean@4.73.0" \
  delete --yes <tag-name> --stateless
```

Observed result:

```text
created native DigitalOcean tag=doplane-native-tag-1783438907
api confirmed tag=doplane-native-tag-1783438907 total_resources=0
native pulumi delete output:
This will delete digitalocean:index/tag:Tag "doplane-native-tag-1783438907".
native tag absent after pulumi delete
```

This narrows the issue: `pulumi do delete --stateless` can delete a native
Pulumi provider resource, but the tested Any Terraform Provider resources
returned success without deleting the remote object.

## AWS tfbridge Control Test

AWS is a normal Pulumi tfbridge provider, so it is a useful control for the
question: does this fail only for non-Pulumi Terraform providers, or also for
Terraform providers loaded through Any Terraform Provider?

Native Pulumi AWS provider:

```sh
pulumi do aws:iam/policy:Policy \
  --package "aws@7.34.0" \
  create --stateless --yes --input-file input.pp

pulumi do aws:iam/policy:Policy \
  --package "aws@7.34.0" \
  delete --yes <policy-arn> --stateless
```

Observed result:

```text
dry-run ok policy=doplane-native-iam-policy-1783440048
created IAM policy name=doplane-native-iam-policy-1783440048 arn=arn:aws:iam::052848974346:policy/doplane-native-iam-policy-1783440048
aws api confirmed policy exists
pulumi delete output:
This will delete aws:iam/policy:Policy "arn:aws:iam::052848974346:policy/doplane-native-iam-policy-1783440048".
policy absent after pulumi delete
```

Any Terraform Provider loading Terraform AWS:

```sh
pulumi do aws:index/iamPolicy:IamPolicy \
  --package "terraform-provider@1.1.4 hashicorp/aws 6.0.0" \
  create --stateless --yes --input-file input.pp

pulumi do aws:index/iamPolicy:IamPolicy \
  --package "terraform-provider@1.1.4 hashicorp/aws 6.0.0" \
  delete --yes <policy-arn> --stateless
```

Observed result:

```text
dry-run ok policy=doplane-anytf-iam-policy-1783440253
created AnyTF IAM policy name=doplane-anytf-iam-policy-1783440253 arn=arn:aws:iam::052848974346:policy/doplane-anytf-iam-policy-1783440253
aws api confirmed policy exists
pulumi delete output:
This will delete aws:index/iamPolicy:IamPolicy "arn:aws:iam::052848974346:policy/doplane-anytf-iam-policy-1783440253".
policy remained after pulumi delete arn=arn:aws:iam::052848974346:policy/doplane-anytf-iam-policy-1783440253
aws api cleanup exit=0 arn=arn:aws:iam::052848974346:policy/doplane-anytf-iam-policy-1783440253
```

This confirms the native tfbridge AWS provider works, while Terraform AWS
loaded through Any Terraform Provider shows the same no-op delete behavior as
Vercel and DigitalOcean.

## Why This Blocks Implementation

The operator finalizer contract depends on delete being trustworthy. If the
runner reports success and the controller removes the finalizer, Kubernetes
will delete the `DoResource` while the external resource remains.

Delete verification could reduce the blast radius, but it is not enough to
make Any Terraform Provider a supported path yet:

- Some Terraform provider resources may not support reliable read/import.
- Verification adds provider-specific edge cases before the main provider UX is
  stable.
- The current issue appears in two unrelated providers, Vercel and
  DigitalOcean.

## Revisit Criteria

Reconsider Any Terraform Provider support when one of these is true:

- `pulumi do --stateless delete` is fixed for parameterized
  `terraform-provider` packages.
- `pulumi do delete` supports passing prior state from `status.outputs`.
- The operator has delete verification, compatibility gates and a clear
  `DeleteVerificationFailed` condition.

Minimum retest matrix:

- Vercel `vercel:index/project:Project`
- DigitalOcean `digitalocean:index/tag:Tag`
- AWS Any Terraform Provider `aws:index/iamPolicy:IamPolicy`
- Native Pulumi AWS provider `aws:iam/policy:Policy` as the tfbridge control
- One Terraform provider resource with read/import support
- One Terraform provider resource without read/import support

## Parked Implementation Notes

If this becomes viable later, the implementation needs these changes:

- Parse package references such as
  `terraform-provider@1.1.4 vercel/vercel 5.3.0`.
- Fetch schemas with:
  `pulumi package get-schema terraform-provider@1.1.4 -- vercel/vercel 5.3.0`.
- Keep CRUD passing the full package string as one `--package` value.
- Preserve schema cache keys using the full raw package string and resource
  token.
- Mount the writable plugin cache as the whole `PULUMI_HOME`, because Any
  Terraform Provider uses both `$PULUMI_HOME/plugins` and
  `$PULUMI_HOME/dynamic_tf_plugins`.
- Preserve `parameterization` metadata in `PackageSchema` for status and
  support output.
