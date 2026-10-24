ALTER TABLE budget_lines ADD COLUMN IF NOT EXISTS link_budget_id BIGINT NULL;

ALTER TABLE budget_lines ADD COLUMN IF NOT EXISTS link_section ENUM('income', 'expense', 'allocation') NULL;

ALTER TABLE budget_lines ADD COLUMN IF NOT EXISTS link_name VARCHAR(255) NULL;

ALTER TABLE budget_lines ADD INDEX IF NOT EXISTS budget_lines_link (link_budget_id, link_section, link_name);

ALTER TABLE budget_lines ADD CONSTRAINT budget_lines_link_budget FOREIGN KEY IF NOT EXISTS (link_budget_id) REFERENCES budgets(id) ON DELETE SET NULL;
