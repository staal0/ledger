-- The amount a fill from the bank last wrote to a line's month. While the
-- budget still has that amount, it is the bank's and a later fill updates it.
CREATE TABLE IF NOT EXISTS bank_filled (
  line_id BIGINT NOT NULL,
  month TINYINT NOT NULL,
  amount DECIMAL(12,2) NOT NULL,
  PRIMARY KEY (line_id, month),
  FOREIGN KEY (line_id) REFERENCES budget_lines(id) ON DELETE CASCADE
);
