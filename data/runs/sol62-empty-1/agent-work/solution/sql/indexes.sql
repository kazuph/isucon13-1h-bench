ALTER TABLE icons ADD UNIQUE INDEX icon_user (user_id), ADD COLUMN image_hash CHAR(64) GENERATED ALWAYS AS (SHA2(image, 256)) STORED;
ALTER TABLE themes ADD INDEX theme_user (user_id);
ALTER TABLE reservation_slots ADD INDEX reservation_time (start_at, end_at);
ALTER TABLE livestreams ADD INDEX livestream_user (user_id);
ALTER TABLE livestream_tags ADD INDEX tag_stream (tag_id, livestream_id), ADD INDEX stream_tag (livestream_id, tag_id);
ALTER TABLE livestream_viewers_history ADD INDEX stream_viewer (livestream_id, user_id);
ALTER TABLE livecomment_reports ADD INDEX report_stream (livestream_id);
ALTER TABLE ng_words ADD INDEX ng_stream_user (livestream_id, user_id);
ALTER TABLE reactions ADD INDEX reaction_stream (livestream_id, created_at), ADD INDEX reaction_emoji (livestream_id, emoji_name);
ALTER TABLE livecomments ADD INDEX comment_stream (livestream_id, created_at), ADD INDEX comment_tip (livestream_id, tip);
CREATE TABLE moderated_livecomments LIKE livecomments;
CREATE TRIGGER archive_moderated_comment BEFORE DELETE ON livecomments
FOR EACH ROW INSERT INTO moderated_livecomments (id, user_id, livestream_id, comment, tip, created_at)
VALUES (OLD.id, OLD.user_id, OLD.livestream_id, OLD.comment, OLD.tip, OLD.created_at);
