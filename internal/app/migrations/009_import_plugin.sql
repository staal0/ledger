-- The bank plugin is now the import plugin: it keeps its migrations and its setting.
UPDATE schema_migrations SET name = CONCAT('import/', SUBSTRING(name, 6)) WHERE name LIKE 'bank/%';
UPDATE settings SET name = 'plugin.import' WHERE name = 'plugin.bank';
