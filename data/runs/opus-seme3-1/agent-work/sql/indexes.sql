ALTER TABLE icons ADD INDEX idx_user (user_id);
ALTER TABLE livestream_viewers_history ADD INDEX idx_ul (user_id, livestream_id);
ALTER TABLE reservation_slots ADD INDEX idx_se (start_at, end_at);
