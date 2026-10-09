# ADR-0025: Dashboards on one repo share one GitHub sync

Every dashboard process polls GitHub itself, and dashboards open on the same repo elect one syncer per poll through a file lock beside the repo's on-disk cache. An unforced sync takes the lock and reuses the cache when every section it shows was synced within half a poll; otherwise it fetches, and saves before releasing the lock. A sync that waits more than 45 seconds for the lock, or is cancelled, fetches for itself, and `r`/`⇧R` always fetch. No separate sync process or machine-wide quota budget exists.
