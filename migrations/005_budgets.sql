CREATE TABLE IF NOT EXISTS budgets (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  slug VARCHAR(100) NOT NULL UNIQUE,
  owner_id BIGINT NULL,
  everyone BOOLEAN NOT NULL DEFAULT FALSE,
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE SET NULL
);

INSERT IGNORE INTO budgets (id, name, slug, everyone, is_default) VALUES (1, 'Budget', 'budget', TRUE, TRUE);

ALTER TABLE budget_years ADD COLUMN IF NOT EXISTS budget_id BIGINT NOT NULL DEFAULT 1 AFTER id;

ALTER TABLE budget_years ALTER COLUMN budget_id DROP DEFAULT;

ALTER TABLE budget_years DROP INDEX IF EXISTS year;

ALTER TABLE budget_years ADD UNIQUE INDEX IF NOT EXISTS budget_year (budget_id, year);

ALTER TABLE budget_years ADD CONSTRAINT budget_years_budget FOREIGN KEY IF NOT EXISTS (budget_id) REFERENCES budgets(id) ON DELETE CASCADE;

ALTER TABLE budget_lines CHANGE COLUMN IF EXISTS budget_id year_id BIGINT NOT NULL;

CREATE TABLE IF NOT EXISTS budget_members (
  budget_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  PRIMARY KEY (budget_id, user_id),
  FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS budget_groups (
  budget_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (budget_id, name),
  FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS user_groups (
  user_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  PRIMARY KEY (user_id, name),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
