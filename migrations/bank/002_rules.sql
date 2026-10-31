-- A rule assigns the transactions whose text contains pattern (in any case)
-- to the line named section and line_name in the transaction's year, or
-- ignores them. pattern is unique per budget in any case.
CREATE TABLE IF NOT EXISTS bank_rules (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  budget_id BIGINT NOT NULL,
  pattern VARCHAR(255) NOT NULL,
  section ENUM('income', 'expense', 'allocation') NULL,
  line_name VARCHAR(255) NULL,
  ignored BOOLEAN NOT NULL DEFAULT FALSE,
  UNIQUE KEY bank_rules_pattern (budget_id, pattern),
  FOREIGN KEY (budget_id) REFERENCES budgets(id) ON DELETE CASCADE
);

-- The rule that assigned a transaction; NULL when it was assigned by hand or not at all.
ALTER TABLE bank_transactions ADD COLUMN IF NOT EXISTS rule_id BIGINT NULL;

ALTER TABLE bank_transactions ADD CONSTRAINT bank_transactions_rule FOREIGN KEY IF NOT EXISTS (rule_id) REFERENCES bank_rules(id) ON DELETE SET NULL;
