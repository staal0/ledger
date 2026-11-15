-- A transaction split into parts, each to a line or ignored; the parts add up to it.
CREATE TABLE IF NOT EXISTS bank_splits (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  transaction_id BIGINT NOT NULL,
  line_id BIGINT NULL,
  ignored BOOLEAN NOT NULL DEFAULT FALSE,
  amount DECIMAL(12,2) NOT NULL,
  INDEX (transaction_id),
  INDEX (line_id),
  FOREIGN KEY (transaction_id) REFERENCES bank_transactions(id) ON DELETE CASCADE,
  FOREIGN KEY (line_id) REFERENCES budget_lines(id) ON DELETE SET NULL
);

ALTER TABLE bank_transactions ADD COLUMN IF NOT EXISTS split BOOLEAN NOT NULL DEFAULT FALSE;
