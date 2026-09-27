---
layout: home

hero:
  name: admin-project-tool
  text: Manifest-driven ./admin
  tagline: A short admin.toml plus a library of composable archetypes drives a self-contained Go task runner for any project.
  actions:
    - theme: brand
      text: Architecture
      link: /architecture/design
    - theme: alt
      text: GitHub
      link: https://github.com/McBrideMusings/admin-project-tool

features:
  - title: Manifest is truth
    details: admin.toml fully describes the runner. ./admin is a generated artifact; regeneration is idempotent.
  - title: Archetypes as mixins
    details: List multiple archetypes per project. The generator merges command sets — no "one giant template" bottleneck.
  - title: Zero contributor install
    details: admin is a single static Go binary. Contributors just clone and run — no interpreter, no venv.
---
