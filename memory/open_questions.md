# Open Questions

- Exact daily run time if `08:00 Europe/Moscow` should change.
- Does alwaysdata Public Cloud `/home/<account>` storage provide the POSIX
  byte-range locking, atomic rename, and fsync guarantees needed by a
  single-process SQLite database in `DELETE` journal mode?
- What is the measured peak RSS of `serve-all` on the real 256 MB Service during
  an overview, `/search`, Workspace publish, and their overlap? Start with
  `GOMEMLIMIT=160MiB` and tune only from measurements.
