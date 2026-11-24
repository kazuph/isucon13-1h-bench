ALTER TABLE icons ADD UNIQUE INDEX icons_user (user_id),
  ADD COLUMN image_hash CHAR(64) GENERATED ALWAYS AS (SHA2(image, 256)) STORED;
ALTER TABLE themes ADD INDEX themes_user (user_id);
ALTER TABLE livestreams ADD INDEX livestreams_user (user_id);
ALTER TABLE reservation_slots ADD INDEX slots_start_end (start_at, end_at);
ALTER TABLE livestream_tags ADD INDEX tags_stream (livestream_id, tag_id),
  ADD INDEX tags_search (tag_id, livestream_id);
ALTER TABLE livestream_viewers_history ADD INDEX viewers_stream_user (livestream_id, user_id);
ALTER TABLE livecomments ADD INDEX comments_stream_time (livestream_id, created_at DESC),
  ADD INDEX comments_stream_tip (livestream_id, tip);
ALTER TABLE livecomment_reports ADD INDEX reports_stream (livestream_id);
ALTER TABLE ng_words ADD INDEX ng_words_stream_user (livestream_id, user_id);
ALTER TABLE reactions ADD INDEX reactions_stream_time (livestream_id, created_at DESC),
  ADD INDEX reactions_stream_emoji (livestream_id, emoji_name);
ALTER TABLE isudns.records ADD INDEX records_name_type (name, type);
