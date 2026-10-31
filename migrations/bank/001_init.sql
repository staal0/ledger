CREATE TABLE IF NOT EXISTS bank_accounts (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  budget_id BIGINT NOT NULL,
  name VARCHAR(100) NOT NULL,
  format VARCHAR(32) NOT NULL,
  FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS bank_imports (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  account_id BIGINT NOT NULL,
  user_id BIGINT NULL,
  imported_at DATETIME NOT NULL,
  added INT NOT NULL,
  skipped INT NOT NULL,
  FOREIGN KEY (account_id) REFERENCES bank_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
);

-- text is compared exactly (utf8mb4_bin), so that the unique key only treats
-- the very same row as imported before.
CREATE TABLE IF NOT EXISTS bank_transactions (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  account_id BIGINT NOT NULL,
  import_id BIGINT NOT NULL,
  booked DATE NOT NULL,
  text VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  amount DECIMAL(12,2) NOT NULL,
  seq SMALLINT NOT NULL,
  line_id BIGINT NULL,
  ignored BOOLEAN NOT NULL DEFAULT FALSE,
  UNIQUE KEY bank_transactions_row (account_id, booked, amount, text, seq),
  INDEX (line_id),
  FOREIGN KEY (account_id) REFERENCES bank_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY (import_id) REFERENCES bank_imports(id) ON DELETE CASCADE,
  FOREIGN KEY (line_id) REFERENCES budget_lines(id) ON DELETE SET NULL
);
