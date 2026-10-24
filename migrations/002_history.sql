CREATE TABLE IF NOT EXISTS budget_history (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  line_id BIGINT NOT NULL,
  field TINYINT NOT NULL,
  old_value VARCHAR(255) NOT NULL,
  new_value VARCHAR(255) NOT NULL,
  user_id BIGINT NULL,
  changed_at DATETIME NOT NULL,
  INDEX (line_id, field, id),
  FOREIGN KEY (line_id) REFERENCES budget_lines(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
);
