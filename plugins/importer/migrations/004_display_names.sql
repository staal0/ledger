-- The name a rule's transactions are shown with, instead of the bank's text.
ALTER TABLE bank_rules ADD COLUMN IF NOT EXISTS display_name VARCHAR(255) NULL;
