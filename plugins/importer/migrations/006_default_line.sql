-- The line an account's transactions go to when no rule fits.
ALTER TABLE bank_accounts
  ADD COLUMN IF NOT EXISTS default_section ENUM('income', 'expense', 'allocation') NULL,
  ADD COLUMN IF NOT EXISTS default_line_name VARCHAR(255) NULL;

-- Assigned by the account's default line, so it follows when that changes.
ALTER TABLE bank_transactions ADD COLUMN IF NOT EXISTS by_default BOOLEAN NOT NULL DEFAULT FALSE;
