---
layout: home

hero:
  name: admin-project-tool
  text: Manifest-driven ./admin
  tagline: A short admin.toml plus a library of composable archetypes generates a self-contained Python task runner for any project.
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
    details: Generated ./admin is a self-contained stdlib-only Python 3.11+ script. Contributors just clone and run.
---
