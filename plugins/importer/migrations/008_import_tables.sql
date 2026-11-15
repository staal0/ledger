-- The tables are named after the plugin, which imports receipts too.
RENAME TABLE IF EXISTS bank_accounts TO import_accounts;
RENAME TABLE IF EXISTS bank_imports TO import_imports;
RENAME TABLE IF EXISTS bank_transactions TO import_transactions;
RENAME TABLE IF EXISTS bank_rules TO import_rules;
RENAME TABLE IF EXISTS bank_filled TO import_filled;
RENAME TABLE IF EXISTS bank_splits TO import_splits;
RENAME TABLE IF EXISTS bank_receipt_items TO import_receipt_items;
