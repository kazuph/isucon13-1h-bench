ALTER TABLE users
    ADD COLUMN theme_id BIGINT NULL,
    ADD COLUMN dark_mode BOOLEAN NULL,
    ADD COLUMN icon_hash CHAR(64) CHARACTER SET ascii NULL;

UPDATE users u
LEFT JOIN themes t ON t.user_id = u.id
LEFT JOIN icons i ON i.user_id = u.id
SET u.theme_id = t.id, u.dark_mode = t.dark_mode, u.icon_hash = i.image_hash;

CREATE TRIGGER themes_insert_profile AFTER INSERT ON themes FOR EACH ROW
    UPDATE users SET theme_id = NEW.id, dark_mode = NEW.dark_mode WHERE id = NEW.user_id;

CREATE TRIGGER themes_update_profile AFTER UPDATE ON themes FOR EACH ROW
    UPDATE users SET theme_id = NEW.id, dark_mode = NEW.dark_mode WHERE id = NEW.user_id;

CREATE TRIGGER themes_delete_profile AFTER DELETE ON themes FOR EACH ROW
    UPDATE users SET theme_id = NULL, dark_mode = NULL WHERE id = OLD.user_id;
