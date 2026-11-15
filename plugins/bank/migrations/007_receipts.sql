-- A receipt is a transaction in a receipts account, with its id there and its items.
-- by_receipt: split by the receipt's items, or ignored as a receipt covers it; redone with the rules.
ALTER TABLE bank_transactions
  ADD COLUMN IF NOT EXISTS receipt VARCHAR(64) NULL,
  ADD COLUMN IF NOT EXISTS by_receipt BOOLEAN NOT NULL DEFAULT FALSE,
  ADD UNIQUE KEY IF NOT EXISTS bank_transactions_receipt (account_id, receipt);

CREATE TABLE IF NOT EXISTS bank_receipt_items (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  transaction_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL,
  amount DECIMAL(12,2) NOT NULL,
  FOREIGN KEY (transaction_id) REFERENCES bank_transactions(id) ON DELETE CASCADE
);
