-- Complete schema of the business database: running it on an empty database creates every table at its latest shape.
-- Rules (directories, 0_schema.sql / 1_baseline.sql / incremental scripts): docs/guides/domain-model.md, "Schema".
-- Table design rules (naming, primary keys, character set, time, indexes): docs/practices/database.md.
-- Every environment (local included) runs it by hand, for example:
--   mysql -h127.0.0.1 -uroot -p <database> < migrations/business/0_schema.sql

-- The mysql client's default character set is not always utf8mb4 (often latin1); without this, non-ASCII text in
-- the script is stored garbled.
SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- Example: delete after forking (through "Example end")
-- todo: to-do items. content is optional; empty string when not given.
CREATE TABLE IF NOT EXISTS todo (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    title      VARCHAR(200)    NOT NULL,
    content    VARCHAR(2000)   NOT NULL DEFAULT '',
    completed  TINYINT(1)      NOT NULL DEFAULT 0,
    created_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    KEY idx_completed (completed)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
-- Example end
