<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

# Text users read

Read this before writing an error message, an `internal/i18n/` entry, a `message` returned to callers, or any other
text an end user reads.

- **A complete, formal sentence saying what happened and what the user can do now.** The reader cannot see the code;
  a sentence that stops at what went wrong leaves them stuck.
- **No implementation words**: counters, queues, processes, retries, table or field names. They mean nothing to the
  reader and leak how the service is built; developer detail goes to logs and comments.
- **No first person, questions or colloquial verbs.** The text speaks for the service, in one register everywhere.
- **Domain terms are used as they are**, the words users already see elsewhere in the product.
