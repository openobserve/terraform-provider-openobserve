# OpenObserve Terraform skill

A Claude skill for writing, reviewing and debugging Terraform or OpenTofu
configuration that uses the
[openobserve/openobserve](https://registry.terraform.io/providers/openobserve/openobserve/latest)
provider.

Everything in here was verified against a live OpenObserve server. Where a rule
is stated, it is because the server rejected the alternative.

## Installing

Copy the folder into your skills directory:

```bash
# For your user, available in every project
mkdir -p ~/.claude/skills
cp -r skills/openobserve-terraform ~/.claude/skills/

# Or for one repository, shared with everyone who clones it
mkdir -p .claude/skills
cp -r skills/openobserve-terraform .claude/skills/
```

Claude loads `SKILL.md` when a task looks like it involves the provider, and
pulls in the reference files it needs from there. You can also invoke it
explicitly by name.

## What is in it

```
openobserve-terraform/
├── SKILL.md                  entry point: mental model, inventory, routing
├── references/
│   ├── streams-and-folders.md    streams, retention, partitioning, folders, orgs
│   ├── dashboards.md             panel JSON, casing, filters, chart bindings
│   ├── alerts.md                 all four query types, thresholds, per-group
│   ├── composite-alerts.md       boolean expressions over other alerts
│   ├── slos.md                   indicators, error budgets, burn-rate alerts
│   ├── pipelines.md              pipelines, VRL functions, pipeline destinations
│   ├── iam.md                    users, service accounts, roles, groups
│   ├── import-and-drift.md       adoption, reconciliation, plans that will not settle
│   ├── errors.md                 server errors decoded, with fixes
│   └── alert-library.md          installing github.com/openobserve/o2-alerts-library
├── examples/
│   ├── complete-stack/           stream, dashboard, SLO, four kinds of alert
│   ├── alert-families/           every alert family side by side
│   ├── composite-alerts/         AND, NOT, nesting, and inspection
│   ├── slos/                     all three indicators, both alert kinds
│   └── alert-library/            install the community library from its manifest
└── scripts/
    └── dev-server.sh             throwaway OpenObserve for verification
```

Every `examples/` directory is a standalone root module. Credentials come from
the environment, so they run as-is:

```bash
eval "$(scripts/dev-server.sh start | grep export)"
cd examples/complete-stack
terraform init && terraform apply -parallelism=1
```

## Registries

The provider is published to both, at the same `source` address:

| Registry | Page |
|---|---|
| Terraform | `registry.terraform.io/providers/openobserve/openobserve` |
| OpenTofu | `search.opentofu.org/provider/openobserve/openobserve` |

It speaks plugin protocol 6.0, so it needs Terraform 1.0+ or any OpenTofu
version.

## Keeping it current

The reference files describe provider 1.3.0. When the provider adds a resource
or an attribute:

1. Update the inventory table in `SKILL.md`.
2. Update the relevant `references/` file, and add the failure mode as well as
   the syntax. The failure modes are the part that is hard to rediscover.
3. Add or extend an example, and apply it against a real server before
   committing. An example that has not been applied is a guess.
4. Add any new server error to `references/errors.md` with the exact message
   fragment.
